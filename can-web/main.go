package main

import (
	"context"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
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

/* =========================
   CAN MAP STRUCTURES
========================= */

type SignalDef struct {
	FrameID    uint32
	FrameName  string
	SignalName string
	StartBit   int
	BitLength  int
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

/* =========================
   RUNTIME STATE
========================= */

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
	mu        sync.RWMutex
	signals   map[string]SignalValue
	rawFrames []RawFrame
}

func NewStore() *Store {
	return &Store{
		signals: make(map[string]SignalValue),
	}
}

func (s *Store) UpdateSignal(v SignalValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := v.FrameName + "." + v.Name
	s.signals[key] = v
}

func (s *Store) PushRaw(r RawFrame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rawFrames = append(s.rawFrames, r)
	if len(s.rawFrames) > 200 {
		s.rawFrames = s.rawFrames[len(s.rawFrames)-200:]
	}
}

func (s *Store) Snapshot() ([]SignalValue, []RawFrame) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sigs := make([]SignalValue, 0, len(s.signals))
	for _, v := range s.signals {
		sigs = append(sigs, v)
	}
	sort.Slice(sigs, func(i, j int) bool {
		if sigs[i].FrameName == sigs[j].FrameName {
			return sigs[i].Name < sigs[j].Name
		}
		return sigs[i].FrameName < sigs[j].FrameName
	})

	raw := append([]RawFrame(nil), s.rawFrames...)
	return sigs, raw
}

/* =========================
   MAIN
========================= */

func main() {
	iface := getenv("CAN_IFACE", "vcan0")
	addr := getenv("HTTP_ADDR", "127.0.0.1:8080")
	mapPath := getenv("CAN_MAP", "canmap.csv")

	frames, err := LoadCANMap(mapPath)
	if err != nil {
		log.Fatalf("CAN map error: %v", err)
	}

	store := NewStore()
	ctx := context.Background()

	go RunCANReader(ctx, iface, frames, store)

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(filepath.Join(".", "web"))))

	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		s, raw := store.Snapshot()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"time":    time.Now(),
			"iface":   iface,
			"signals": s,
			"raw":     raw,
		})
	})

	log.Printf("Listening on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

/* =========================
   SOCKETCAN READER (EINRIDE)
========================= */

func RunCANReader(ctx context.Context, iface string, defs map[uint32]FrameDef, store *Store) {
	conn, err := socketcan.DialContext(ctx, "can", iface)
	if err != nil {
		log.Fatalf("socketcan: %v", err)
	}
	defer conn.Close()

	recv := socketcan.NewReceiver(conn)

	for recv.Receive() {
		f := recv.Frame()
		data := f.Data[:f.Length]

		store.PushRaw(RawFrame{
			TS:        time.Now(),
			ID:        fmt.Sprintf("0x%03X", f.ID),
			DLC:       int(f.Length),
			DataHex:   strings.ToUpper(hex.EncodeToString(data)),
			DataASCII: ascii(data),
		})

		def, ok := defs[uint32(f.ID)]
		if !ok {
			continue
		}

		for _, sig := range def.Signals {
			val := decodeSignal(f.Data, sig)
			store.UpdateSignal(SignalValue{
				Name:      sig.SignalName,
				Value:     val,
				Unit:      sig.Unit,
				FrameID:   fmt.Sprintf("0x%03X", f.ID),
				FrameName: def.Name,
				UpdatedAt: time.Now(),
				Dir:       sig.Direction,
				Comment:   sig.Comment,
			})
		}
	}

	if err := recv.Err(); err != nil {
		log.Fatalf("CAN recv error: %v", err)
	}
}

/* =========================
   SIGNAL DECODING
========================= */

func decodeSignal(d can.Data, s SignalDef) float64 {
	start := uint8(s.StartBit)
	length := uint8(s.BitLength)

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
	}
	return clamp(raw*s.Factor + s.Offset)
}

func clamp(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

/* =========================
   CAN MAP LOADER
========================= */

func LoadCANMap(path string) (map[uint32]FrameDef, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}

	h := make(map[string]int)
	for i, c := range rows[0] {
		h[c] = i
	}

	frames := make(map[uint32]FrameDef)

	for _, row := range rows[1:] {
		id, _ := strconv.ParseUint(strings.TrimPrefix(row[h["frame_id"]], "0x"), 16, 32)

		def := SignalDef{
			FrameID:    uint32(id),
			FrameName:  row[h["frame_name"]],
			SignalName: row[h["signal_name"]],
			StartBit:   atoi(row[h["start_bit"]]),
			BitLength:  atoi(row[h["bit_length"]]),
			Endianness: Endianness(row[h["endianness"]]),
			Signed:     strings.ToLower(row[h["signed"]]) == "true",
			Factor:     atof(row[h["factor"]]),
			Offset:     atof(row[h["offset"]]),
			Unit:       row[h["unit"]],
			Direction:  row[h["direction"]],
			Comment:    row[h["comment"]],
		}

		fd := frames[uint32(id)]
		fd.ID = uint32(id)
		fd.Name = def.FrameName
		fd.Signals = append(fd.Signals, def)
		frames[uint32(id)] = fd
	}
	return frames, nil
}

/* =========================
   UTILS
========================= */

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func atoi(s string) int {
	i, _ := strconv.Atoi(s)
	return i
}

func atof(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func ascii(b []byte) string {
	out := make([]rune, len(b))
	for i, c := range b {
		if c >= 32 && c <= 126 {
			out[i] = rune(c)
		} else {
			out[i] = '.'
		}
	}
	return string(out)
}
