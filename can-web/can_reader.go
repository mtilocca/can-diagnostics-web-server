package main

import (
	"context"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.einride.tech/can"
	"go.einride.tech/can/pkg/socketcan"
)

type Endianness string

const (
	EndianLittle Endianness = "little"
	EndianBig    Endianness = "big"
)

type SignalDef struct {
	FrameID    uint32
	FrameName  string
	SignalName string
	StartBit   uint8
	BitLength  uint8
	Endianness Endianness
	Signed     bool
	Factor     float64
	Offset     float64
	Unit       string
	Direction  string
	Comment    string
}

type FrameDef struct {
	ID      uint32
	Name    string
	Signals []SignalDef
}

type SignalValue struct {
	Name      string    `json:"name"`
	Value     float64   `json:"value"`
	Unit      string    `json:"unit"`
	FrameID   string    `json:"frame_id"`
	FrameName string    `json:"frame_name"`
	UpdatedAt time.Time `json:"updated_at"`
	Dir       string    `json:"direction"`
	Comment   string    `json:"comment"`
}

type RawFrame struct {
	TS        time.Time `json:"ts"`
	ID        string    `json:"id"`
	DLC       int       `json:"dlc"`
	DataHex   string    `json:"data_hex"`
	DataASCII string    `json:"data_ascii"`
}

type Store struct {
	mu          sync.RWMutex
	signals     map[string]SignalValue
	rawFrames   []RawFrame
	rawCapacity int
}

func NewStore(rawCapacity int) *Store {
	return &Store{
		signals:     make(map[string]SignalValue),
		rawCapacity: rawCapacity,
	}
}

func (s *Store) UpsertSignal(v SignalValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%s.%s", v.FrameName, v.Name)
	s.signals[key] = v
}

func (s *Store) PushRaw(r RawFrame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rawFrames = append(s.rawFrames, r)
	if len(s.rawFrames) > s.rawCapacity {
		s.rawFrames = s.rawFrames[len(s.rawFrames)-s.rawCapacity:]
	}
}

func (s *Store) Snapshot() (signals []SignalValue, raw []RawFrame) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	signals = make([]SignalValue, 0, len(s.signals))
	for _, v := range s.signals {
		signals = append(signals, v)
	}
	sort.Slice(signals, func(i, j int) bool {
		if signals[i].FrameName == signals[j].FrameName {
			return signals[i].Name < signals[j].Name
		}
		return signals[i].FrameName < signals[j].FrameName
	})

	raw = make([]RawFrame, len(s.rawFrames))
	copy(raw, s.rawFrames)
	return
}

func RunCANReader(ctx context.Context, iface string, defs map[uint32]FrameDef, store *Store) error {
	conn, err := socketcan.DialContext(ctx, "can", iface)
	if err != nil {
		return fmt.Errorf("socketcan dial(%s): %w", iface, err)
	}
	defer conn.Close()

	recv := socketcan.NewReceiver(conn)
	log.Printf("CAN reader listening on %s", iface)

	for recv.Receive() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		f := recv.Frame()
		frameID := uint32(f.ID)
		dlc := int(f.Length)
		data := f.Data[:dlc]

		store.PushRaw(RawFrame{
			TS:        time.Now(),
			ID:        fmt.Sprintf("0x%03X", frameID),
			DLC:       dlc,
			DataHex:   strings.ToUpper(hex.EncodeToString(data)),
			DataASCII: safeASCII(data),
		})

		def, ok := defs[frameID]
		if !ok {
			continue
		}

		for _, sig := range def.Signals {
			val := decodeSignal(f.Data, sig)
			store.UpsertSignal(SignalValue{
				Name:      sig.SignalName,
				Value:     clampFinite(val),
				Unit:      sig.Unit,
				FrameID:   fmt.Sprintf("0x%03X", frameID),
				FrameName: def.Name,
				UpdatedAt: time.Now(),
				Dir:       sig.Direction,
				Comment:   sig.Comment,
			})
		}
	}

	if err := recv.Err(); err != nil {
		return fmt.Errorf("receiver error: %w", err)
	}
	return nil
}

func decodeSignal(d can.Data, s SignalDef) float64 {
	start := s.StartBit
	length := s.BitLength

	var raw float64
	switch s.Endianness {
	case EndianLittle:
		if s.Signed {
			raw = float64(d.SignedBitsLittleEndian(start, length))
		} else {
			raw = float64(d.UnsignedBitsLittleEndian(start, length))
		}
	case EndianBig:
		if s.Signed {
			raw = float64(d.SignedBitsBigEndian(start, length))
		} else {
			raw = float64(d.UnsignedBitsBigEndian(start, length))
		}
	default:
		raw = 0
	}
	return raw*s.Factor + s.Offset
}

func clampFinite(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

func safeASCII(b []byte) string {
	out := make([]rune, 0, len(b))
	for _, c := range b {
		if c >= 32 && c <= 126 {
			out = append(out, rune(c))
		} else {
			out = append(out, '.')
		}
	}
	return string(out)
}

// ---------------- CSV loader (same behavior as before) ----------------

func LoadCANMap(path string) (map[uint32]FrameDef, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.TrimLeadingSpace = true

	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("csv has no data rows")
	}

	h := make(map[string]int)
	for i, name := range records[0] {
		h[strings.TrimSpace(name)] = i
	}

	req := []string{"direction", "frame_id", "frame_name", "dlc", "signal_name", "start_bit", "bit_length", "endianness", "signed", "factor", "offset", "unit", "comment"}
	for _, k := range req {
		if _, ok := h[k]; !ok {
			return nil, fmt.Errorf("missing required column: %s", k)
		}
	}

	frames := make(map[uint32]FrameDef)

	for _, row := range records[1:] {
		get := func(k string) string {
			idx := h[k]
			if idx >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[idx])
		}

		frameID, err := parseHexID(get("frame_id"))
		if err != nil {
			return nil, fmt.Errorf("bad frame_id: %w", err)
		}

		startBit64, err := strconv.ParseUint(get("start_bit"), 10, 8)
		if err != nil {
			return nil, fmt.Errorf("bad start_bit: %w", err)
		}
		bitLen64, err := strconv.ParseUint(get("bit_length"), 10, 8)
		if err != nil {
			return nil, fmt.Errorf("bad bit_length: %w", err)
		}

		endianness := Endianness(strings.ToLower(get("endianness")))
		signed := strings.EqualFold(get("signed"), "true")

		factor, err := strconv.ParseFloat(get("factor"), 64)
		if err != nil {
			return nil, fmt.Errorf("bad factor: %w", err)
		}
		offset, err := strconv.ParseFloat(get("offset"), 64)
		if err != nil {
			return nil, fmt.Errorf("bad offset: %w", err)
		}

		frameName := get("frame_name")

		def := SignalDef{
			FrameID:    frameID,
			FrameName:  frameName,
			SignalName: get("signal_name"),
			StartBit:   uint8(startBit64),
			BitLength:  uint8(bitLen64),
			Endianness: endianness,
			Signed:     signed,
			Factor:     factor,
			Offset:     offset,
			Unit:       get("unit"),
			Direction:  strings.ToLower(get("direction")),
			Comment:    get("comment"),
		}

		fd := frames[frameID]
		if fd.ID == 0 {
			fd = FrameDef{ID: frameID, Name: frameName}
		}
		fd.Signals = append(fd.Signals, def)
		frames[frameID] = fd
	}

	for id, fd := range frames {
		sort.Slice(fd.Signals, func(i, j int) bool { return fd.Signals[i].StartBit < fd.Signals[j].StartBit })
		frames[id] = fd
	}

	return frames, nil
}

func parseHexID(s string) (uint32, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "0x")
	u, err := strconv.ParseUint(s, 16, 32)
	return uint32(u), err
}

// keep json import used by other files (avoid unused if you remove later)
var _ = json.RawMessage{}
