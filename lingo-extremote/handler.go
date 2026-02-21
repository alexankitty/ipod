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
}

func ackSuccess(req *ipod.Command) *ACK {
	return &ACK{Status: ACKStatusSuccess, CmdID: req.ID.CmdID()}
}

// func ackPending(req ipod.Packet, maxWait uint32) ACKPending {
// 	return ACKPending{Status: ACKStatusPending, CmdID: uint8(req.ID.CmdID()), MaxWait: maxWait}
// }

func HandleExtRemote(req *ipod.Command, tr ipod.CommandWriter, dev DeviceExtRemote) error {
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
		// Always report Playing — the car issued PlayControl=Play so we are playing.
		// Reporting Paused causes the car to exit iPod mode and stop USB audio.
		ipod.Respond(req, tr, &ReturnPlayStatus{
			TrackLength:   length,
			TrackPosition: pos,
			State:         PlayerStatePlaying,
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
	case *SetPlayStatusChangeNotificationShort:
		ipod.Respond(req, tr, ackSuccess(req))
	case *PlayCurrentSelection:
		ipod.Respond(req, tr, ackSuccess(req))
		// Notify car that track index 0 is now the current track.
		// This prompts the car to query title/artist/album for display.
		ipod.Send(tr, &PlayStatusChangeNotificationTrackIndex{
			EventID:    0x01,
			TrackIndex: 0,
		})
	case *PlayControl:
		ipod.Respond(req, tr, ackSuccess(req))
		// Notify the car that playback is now active
		ipod.Send(tr, &PlayStatusChangeNotification{
			EventID:     0x00, // PlayStatusChanged
			PlayerState: byte(PlayerStatePlaying),
		})
		// Also notify TrackIndexChanged so the car immediately issues
		// PlayCurrentSelection instead of waiting out its internal timer.
		// On a real iPod, after pressing Play the iPod proactively sends
		// this to indicate which track started.
		ipod.Send(tr, &PlayStatusChangeNotificationTrackIndex{
			EventID:    0x01,
			TrackIndex: 0,
		})
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
