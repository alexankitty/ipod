package extremote

import (
	"github.com/oandrew/ipod"
)

type DeviceExtRemote interface {
	// PlaybackStatus returns track duration (ms), current position (ms), and
	// whether the player is currently playing (vs paused/stopped).
	PlaybackStatus() (trackLength, trackPos uint32, playing bool)
	TrackTitle() string
	TrackArtist() string
	TrackAlbum() string
	// MediaControl sends a playback command to the phone via AVRCP.
	// method is a BlueZ MediaPlayer1 method name: "Play", "Pause",
	// "Next", "Previous", "FastForward", "Rewind".
	MediaControl(method string)
}

func ackSuccess(req *ipod.Command) *ACK {
	return &ACK{Status: ACKStatusSuccess, CmdID: req.ID.CmdID()}
}

// ExtRemoteHandler manages session-scoped state for lingo 0x04 (Extended Remote).
// A new instance must be created for each USB session so that playing state
// resets correctly on reconnect.
type ExtRemoteHandler struct {
	// playing is false until the first PlayControl is received.  Keeping it
	// false initially means GetPlayStatus returns Paused, which causes the car
	// to treat the first Toggle as "start playing" → single PlayControl cycle
	// → PlayCurrentSelection arrives well within the car's 3.716-second
	// audio-open window.
	playing bool
}

// NewExtRemoteHandler returns a handler with playing=false (paused initial state).
func NewExtRemoteHandler() *ExtRemoteHandler { return &ExtRemoteHandler{} }

func (h *ExtRemoteHandler) playerState() PlayerState {
	if h.playing {
		return PlayerStatePlaying
	}
	return PlayerStatePaused
}

// HandleExtRemote is kept for callers that don't need session state.
// Prefer ExtRemoteHandler.Handle for new code.
func HandleExtRemote(req *ipod.Command, tr ipod.CommandWriter, dev DeviceExtRemote) error {
	h := ExtRemoteHandler{playing: true} // legacy: assume playing
	return h.Handle(req, tr, dev)
}

func (h *ExtRemoteHandler) Handle(req *ipod.Command, tr ipod.CommandWriter, dev DeviceExtRemote) error {
	//log.Printf("Req: %#v", req)
	switch msg := req.Payload.(type) {

	case *GetCurrentPlayingTrackChapterInfo:
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackChapterInfo{
			CurrentChapterIndex: 0,
			ChapterCount:        1,
		})
	case *SetCurrentPlayingTrackChapter:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetCurrentPlayingTrackChapterPlayStatus:
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackChapterPlayStatus{
			ChapterPosition: 0,
			ChapterLength:   0,
		})
	case *GetCurrentPlayingTrackChapterName:
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackChapterName{
			ChapterName: ipod.StringToBytes("chapter"),
		})
	case *GetAudiobookSpeed:
		ipod.Respond(req, tr, &ReturnAudiobookSpeed{
			Speed: 0,
		})
	case *SetAudiobookSpeed:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetIndexedPlayingTrackInfo:
		var info interface{}
		switch msg.InfoType {
		case TrackInfoCaps:
			capLength := uint32(300_000)
			if dev != nil {
				cl, cp, _ := dev.PlaybackStatus()
				if cp+300_000 > cl {
					cl = cp + 300_000
				}
				capLength = cl
			}
			info = &TrackCaps{
				Caps:         0x0,
				TrackLength:  capLength,
				ChapterCount: 1,
			}
		case TrackInfoDescription, TrackInfoLyrics:
			info = &TrackLongText{
				Flags:       0x0,
				PacketIndex: 0,
				Text:        0x00,
			}
		case TrackInfoArtworkCount:
			info = struct{}{}
		default:
			info = []byte{0x00}

		}
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackInfo{
			InfoType: msg.InfoType,
			Info:     info,
		})
	case *GetArtworkFormats:
		ipod.Respond(req, tr, &RetArtworkFormats{})
	case *GetTrackArtworkData:
		ipod.Respond(req, tr, &ACK{
			Status: ACKStatusFailed,
			CmdID:  req.ID.CmdID(),
		})
	case *ResetDBSelection:
		ipod.Respond(req, tr, ackSuccess(req))
	case *SelectDBRecord:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetNumberCategorizedDBRecords:
		// Return 0 for all categories so the car skips the DB browse loop
		// and proceeds immediately to PlayCurrentSelection(-1). The 2+ second
		// browse loop was causing PlayCurrentSelection to miss the car's 3.7s
		// audio-open deadline and the USB audio interface was being closed.
		ipod.Respond(req, tr, &ReturnNumberCategorizedDBRecords{
			RecordCount: 0,
		})
	case *RetrieveCategorizedDatabaseRecords:
		// Shouldn't be reached when RecordCount=0, but respond gracefully.
		ipod.Respond(req, tr, &ACK{Status: ACKStatusFailed, CmdID: req.ID.CmdID()})
	case *GetPlayStatus:
		length, pos := uint32(300_000), uint32(0)
		if dev != nil {
			length, pos, _ = dev.PlaybackStatus()
		}
		// Live radio streams have positions that grow unboundedly.
		// Ensure length is always ahead of position so the car doesn't
		// think the track has ended.
		if pos+300_000 > length {
			length = pos + 300_000
		}
		ipod.Respond(req, tr, &ReturnPlayStatus{
			TrackLength:   length,
			TrackPosition: pos,
			State:         h.playerState(),
		})
	case *GetCurrentPlayingTrackIndex:
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackIndex{
			TrackIndex: 0,
		})
	case *GetIndexedPlayingTrackTitle:
		title := "Bluetooth"
		if dev != nil {
			title = dev.TrackTitle()
		}
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackTitle{
			Title: ipod.StringToBytes(title),
		})
	case *GetIndexedPlayingTrackArtistName:
		artist := ""
		if dev != nil {
			artist = dev.TrackArtist()
		}
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackArtistName{
			ArtistName: ipod.StringToBytes(artist),
		})
	case *GetIndexedPlayingTrackAlbumName:
		album := ""
		if dev != nil {
			album = dev.TrackAlbum()
		}
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackAlbumName{
			AlbumName: ipod.StringToBytes(album),
		})
	case *SetPlayStatusChangeNotification:
		ipod.Respond(req, tr, ackSuccess(req))
		// Push Paused + TrackIndex(0) so the car knows a track exists but is
		// paused. PlayControl(Toggle) then means "start playing" and the car
		// follows immediately with PlayCurrentSelection in a single toggle
		// cycle — no 2-second debounce gap.
		// (Pushing Playing caused the car to Toggle→Pause first, then wait 2s
		// before Toggle→Play, causing PlayCurrentSelection to miss the 3.716s
		// audio-open deadline.)
		ipod.Send(tr, &PlayStatusChangeNotification{
			EventID:     0x00, // PlayStatusChanged
			PlayerState: byte(PlayerStatePaused),
		})
		ipod.Send(tr, &PlayStatusChangeNotificationTrackIndex{
			EventID:    0x01,
			TrackIndex: 0,
		})
	case *SetPlayStatusChangeNotificationShort:
		ipod.Respond(req, tr, ackSuccess(req))
		ipod.Send(tr, &PlayStatusChangeNotification{
			EventID:     0x00,
			PlayerState: byte(PlayerStatePaused),
		})
		ipod.Send(tr, &PlayStatusChangeNotificationTrackIndex{
			EventID:    0x01,
			TrackIndex: 0,
		})
	case *PlayCurrentSelection:
		h.playing = true
		ipod.Respond(req, tr, ackSuccess(req))
		// Do NOT send another TrackIndexChanged here. The car issued
		// PlayCurrentSelection precisely because it already received our
		// TrackIndexChanged (sent from PlayControl). A second notification
		// would make the car think a new track started mid-play, causing it
		// to immediately Toggle→Pause and re-open audio in a 2-second loop.
	case *PlayControl:
		wasPlaying := h.playing
		// Determine the BlueZ MediaPlayer1 method to call on the phone.
		var avrcpCmd string
		switch msg.Cmd {
		case PlayControlToggle:
			h.playing = !wasPlaying
			if h.playing {
				avrcpCmd = "Play"
			} else {
				avrcpCmd = "Pause"
			}
		case PlayControlPlay:
			h.playing = true
			avrcpCmd = "Play"
		case PlayControlPause:
			h.playing = false
			avrcpCmd = "Pause"
		case PlayControlStop:
			h.playing = false
			avrcpCmd = "Pause"
		case PlayControlNextTrack, PlayControlNext, PlayControlNextChapter:
			avrcpCmd = "Next"
		case PlayControlPrevTrack, PlayControlPrev, PlayControlPrevChapter:
			avrcpCmd = "Previous"
		case PlayControlStartFF:
			avrcpCmd = "FastForward"
		case PlayControlStartRew:
			avrcpCmd = "Rewind"
		case PlayControlEndFFRew:
			avrcpCmd = "Release"
		}
		if avrcpCmd != "" && dev != nil {
			dev.MediaControl(avrcpCmd)
		}
		ipod.Respond(req, tr, ackSuccess(req))
		// Always notify the current state after a PlayControl.
		ipod.Send(tr, &PlayStatusChangeNotification{
			EventID:     0x00,
			PlayerState: byte(h.playerState()),
		})
		// Send TrackIndexChanged only when transitioning TO playing.
		// This prompts the car to issue PlayCurrentSelection (re-opens audio).
		// Sending it on Pause/Toggle-off would confuse the car into thinking a
		// new track started while it's trying to pause.
		if h.playing && !wasPlaying {
			ipod.Send(tr, &PlayStatusChangeNotificationTrackIndex{
				EventID:    0x01,
				TrackIndex: 0,
			})
		}
	case *GetTrackArtworkTimes:
		ipod.Respond(req, tr, &RetTrackArtworkTimes{})
	case *GetShuffle:
		ipod.Respond(req, tr, &ReturnShuffle{Mode: ShuffleOff})
	case *SetShuffle:
		ipod.Respond(req, tr, ackSuccess(req))

	case *GetRepeat:
		ipod.Respond(req, tr, &ReturnRepeat{Mode: RepeatOff})
	case *SetRepeat:
		ipod.Respond(req, tr, ackSuccess(req))

	case *SetDisplayImage:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetMonoDisplayImageLimits:
		ipod.Respond(req, tr, &ReturnMonoDisplayImageLimits{
			MaxWidth:    640,
			MaxHeight:   960,
			PixelFormat: 0x01,
		})
	case *GetNumPlayingTracks:
		ipod.Respond(req, tr, &ReturnNumPlayingTracks{
			NumTracks: 1,
		})
	case *SetCurrentPlayingTrack:
		ipod.Respond(req, tr, ackSuccess(req))
	case *SelectSortDBRecord:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetColorDisplayImageLimits:
		ipod.Respond(req, tr, &ReturnColorDisplayImageLimits{
			MaxWidth:    640,
			MaxHeight:   960,
			PixelFormat: 0x01,
		})
	case *ResetDBSelectionHierarchy:
		ipod.Respond(req, tr, &ACK{Status: ACKStatusFailed, CmdID: req.ID.CmdID()})

	case *GetDBiTunesInfo:
	// RetDBiTunesInfo:
	case *GetUIDTrackInfo:
	// RetUIDTrackInfo:
	case *GetDBTrackInfo:
	// RetDBTrackInfo:
	case *GetPBTrackInfo:
	// RetPBTrackInfo:

	default:
		_ = msg
	}
	return nil
}
