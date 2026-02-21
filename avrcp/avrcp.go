// Package avrcp reads playback state and track metadata from the BlueZ
// org.bluez.MediaPlayer1 D-Bus interface (AVRCP).
package avrcp

import (
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	bluezService = "org.bluez"
	playerIface  = "org.bluez.MediaPlayer1"
	propsIface   = "org.freedesktop.DBus.Properties"
	objMgrIface  = "org.freedesktop.DBus.ObjectManager"
)

// TrackInfo holds metadata for the currently playing track.
type TrackInfo struct {
	Title    string
	Artist   string
	Album    string
	Duration uint32 // milliseconds; 0 if unknown
}

// PlayState is the complete snapshot of AVRCP playback state.
type PlayState struct {
	Track    TrackInfo
	Position uint32 // milliseconds
	Playing  bool
}

// Source polls the BlueZ system D-Bus for AVRCP MediaPlayer1 state.
type Source struct {
	mu    sync.RWMutex
	conn  *dbus.Conn
	state PlayState
}

// NewSource connects to the system D-Bus and starts a background poller.
// Returns an error only if the D-Bus connection itself fails.
func NewSource() (*Source, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}
	s := &Source{conn: conn}
	go s.loop()
	return s, nil
}

// PlaybackStatus implements extremote.DeviceExtRemote.
// Returns (trackLength ms, trackPosition ms, playing).
func (s *Source) PlaybackStatus() (trackLength, trackPos uint32, playing bool) {
	st := s.snapshot()
	dur := st.Track.Duration
	if dur == 0 {
		dur = 300_000 // 5-minute fallback
	}
	return dur, st.Position, st.Playing
}

// TrackTitle implements extremote.DeviceExtRemote.
func (s *Source) TrackTitle() string {
	t := s.snapshot().Track.Title
	if t == "" {
		return "Bluetooth"
	}
	return t
}

// TrackArtist implements extremote.DeviceExtRemote.
func (s *Source) TrackArtist() string {
	return s.snapshot().Track.Artist
}

// TrackAlbum implements extremote.DeviceExtRemote.
func (s *Source) TrackAlbum() string {
	return s.snapshot().Track.Album
}

// snapshot returns a copy of the current state under the read lock.
func (s *Source) snapshot() PlayState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// loop polls BlueZ every 500 ms.
func (s *Source) loop() {
	for {
		s.refresh()
		time.Sleep(500 * time.Millisecond)
	}
}

// findPlayer returns the D-Bus object path of the first MediaPlayer1 found.
func (s *Source) findPlayer() (dbus.ObjectPath, bool) {
	obj := s.conn.Object(bluezService, "/")
	var managed map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := obj.Call(objMgrIface+".GetManagedObjects", 0).Store(&managed); err != nil {
		return "", false
	}
	for path, ifaces := range managed {
		if _, ok := ifaces[playerIface]; ok {
			return path, true
		}
	}
	return "", false
}

// refresh fetches the current player properties and updates the cached state.
func (s *Source) refresh() {
	path, ok := s.findPlayer()
	if !ok {
		return
	}

	obj := s.conn.Object(bluezService, path)
	var props map[string]dbus.Variant
	if err := obj.Call(propsIface+".GetAll", 0, playerIface).Store(&props); err != nil {
		return
	}

	var next PlayState

	if v, ok := props["Position"]; ok {
		if pos, ok := v.Value().(uint32); ok {
			next.Position = pos
		}
	}
	if v, ok := props["Status"]; ok {
		if status, ok := v.Value().(string); ok {
			next.Playing = status == "playing"
		}
	}
	if v, ok := props["Track"]; ok {
		if track, ok := v.Value().(map[string]dbus.Variant); ok {
			if t, ok := track["Title"]; ok {
				next.Track.Title, _ = t.Value().(string)
			}
			if a, ok := track["Artist"]; ok {
				next.Track.Artist, _ = a.Value().(string)
			}
			if a, ok := track["Album"]; ok {
				next.Track.Album, _ = a.Value().(string)
			}
			if d, ok := track["Duration"]; ok {
				next.Track.Duration, _ = d.Value().(uint32)
			}
		}
	}

	s.mu.Lock()
	s.state = next
	s.mu.Unlock()
}
