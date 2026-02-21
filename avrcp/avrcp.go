// Package avrcp reads playback state from the BlueZ org.bluez.MediaPlayer1
// D-Bus interface (AVRCP) by shelling out to busctl. This avoids external
// Go D-Bus library dependencies.
package avrcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// PlayState is the complete snapshot of AVRCP playback state.
type PlayState struct {
	Title    string
	Artist   string
	Album    string
	Duration uint32 // milliseconds; 0 if unknown
	Position uint32 // milliseconds
	Playing  bool
}

// Source polls BlueZ via busctl for AVRCP MediaPlayer1 state.
type Source struct {
	mu    sync.RWMutex
	state PlayState
}

func newPlayState() PlayState {
	// Default to playing=true and a non-zero duration so the car doesn't
	// see a paused/empty state before the first successful AVRCP poll.
	return PlayState{Playing: true, Duration: 300_000}
}

// NewSource starts a background AVRCP poller and returns immediately.
// It never returns an error — polling failures are handled gracefully.
func NewSource() (*Source, error) {
	s := &Source{state: newPlayState()}
	go s.loop()
	return s, nil
}

// PlaybackStatus implements extremote.DeviceExtRemote.
// Returns (trackLength ms, trackPosition ms, playing).
func (s *Source) PlaybackStatus() (trackLength, trackPos uint32, playing bool) {
	st := s.snapshot()
	dur := st.Duration
	if dur == 0 {
		dur = 300_000 // 5-minute fallback when duration unknown
	}
	return dur, st.Position, st.Playing
}

// TrackTitle implements extremote.DeviceExtRemote.
func (s *Source) TrackTitle() string {
	t := s.snapshot().Title
	if t == "" {
		return "Bluetooth"
	}
	return t
}

// TrackArtist implements extremote.DeviceExtRemote.
func (s *Source) TrackArtist() string { return s.snapshot().Artist }

// TrackAlbum implements extremote.DeviceExtRemote.
func (s *Source) TrackAlbum() string { return s.snapshot().Album }

// TrackPositionMs implements dispremote.DeviceDispRemote.
func (s *Source) TrackPositionMs() uint32 { return s.snapshot().Position }

// TrackLengthMs implements dispremote.DeviceDispRemote.
func (s *Source) TrackLengthMs() uint32 {
	st := s.snapshot()
	dur := st.Duration
	if dur == 0 {
		dur = 300_000
	}
	return dur
}

func (s *Source) snapshot() PlayState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Source) loop() {
	for {
		s.refresh()
		time.Sleep(500 * time.Millisecond)
	}
}

// findPlayerPath returns the D-Bus object path of the first BlueZ
// MediaPlayer1 object by parsing `busctl tree org.bluez`.
func findPlayerPath() string {
	out, err := exec.Command("busctl", "--system", "tree", "org.bluez").Output()
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Match leaf player objects like /org/bluez/hci0/dev_.../player0
		if strings.Contains(line, "/player") && !strings.Contains(line, "/player/") &&
			!strings.Contains(line, "NowPlaying") && !strings.Contains(line, "Filesystem") {
			// strip tree-drawing characters
			path := strings.TrimLeft(line, "├─└│ ")
			if strings.HasPrefix(path, "/org/bluez") {
				return path
			}
		}
	}
	return ""
}

// busctlVariant holds the minimal structure returned by busctl --json=short.
type busctlVariant struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// getProperty calls `busctl --system --json=short get-property ...` and returns
// the parsed variant. Returns nil on any error.
func getProperty(path, iface, prop string) *busctlVariant {
	out, err := exec.Command("busctl", "--system", "--json=short",
		"get-property", "org.bluez", path, iface, prop).Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	var v busctlVariant
	if err := json.Unmarshal(bytes.TrimSpace(out), &v); err != nil {
		return nil
	}
	return &v
}

func (s *Source) refresh() {
	path := findPlayerPath()
	if path == "" {
		return
	}

	var next PlayState

	// Position (uint32, milliseconds)
	if v := getProperty(path, "org.bluez.MediaPlayer1", "Position"); v != nil {
		var pos uint32
		if json.Unmarshal(v.Data, &pos) == nil {
			next.Position = pos
		}
	}

	// Status ("playing" | "paused" | "stopped" | "forward-seek" | "reverse-seek" | "error")
	if v := getProperty(path, "org.bluez.MediaPlayer1", "Status"); v != nil {
		var status string
		if json.Unmarshal(v.Data, &status) == nil {
			next.Playing = status == "playing"
		}
	}

	// Track is an a{sv} dict — busctl renders it as a JSON object.
	// We parse Title, Artist, Album, Duration if present.
	if v := getProperty(path, "org.bluez.MediaPlayer1", "Track"); v != nil {
		var track map[string]busctlVariant
		if json.Unmarshal(v.Data, &track) == nil {
			if t, ok := track["Title"]; ok {
				json.Unmarshal(t.Data, &next.Title)
			}
			if a, ok := track["Artist"]; ok {
				json.Unmarshal(a.Data, &next.Artist)
			}
			if a, ok := track["Album"]; ok {
				json.Unmarshal(a.Data, &next.Album)
			}
			if d, ok := track["Duration"]; ok {
				json.Unmarshal(d.Data, &next.Duration)
			}
		}
	}

	// Preserve Playing=true if AVRCP poll didn't return Status (e.g. no Track property)
	if !next.Playing && next.Position == 0 {
		// No useful data from this poll — keep existing state
		return
	}
	s.mu.Lock()
	s.state = next
	s.mu.Unlock()
}
