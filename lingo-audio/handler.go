package audio

import (
	"github.com/oandrew/ipod"
)

type DeviceAudio interface {
}

// func ackSuccess(req ipod.Packet) ACK {
// 	return ACK{Status: ACKStatusSuccess, CmdID: uint8(req.ID.CmdID())}
// }

// func ackPending(req ipod.Packet, maxWait uint32) ACKPending {
// 	return ACKPending{Status: ACKStatusPending, CmdID: uint8(req.ID.CmdID()), MaxWait: maxWait}
// }

func Start(tr ipod.CommandWriter) {
	ipod.Send(tr, &GetAccSampleRateCaps{})
}

func HandleAudio(req *ipod.Command, tr ipod.CommandWriter, dev DeviceAudio) error {
	switch msg := req.Payload.(type) {
	case *AccAck:
		// Car acknowledges our sample rate caps request
		// No additional action needed

	case *RetAccSampleRateCaps:
		// Car sends its supported sample rates
		// Acknowledge receipt
		ipod.Respond(req, tr, &AccAck{
			Status: ACKStatusSuccess,
			CmdID:  0x03, // RetAccSampleRateCaps command ID
		})

	case *TrackNewAudioAttributes:
		// Car sends audio attributes and is ready for audio
		// Acknowledge with AccAck
		ipod.Respond(req, tr, &AccAck{
			Status: ACKStatusSuccess,
			CmdID:  0x04, // TrackNewAudioAttributes command ID
		})

	case *SetVideoDelay:
		// Car sets video delay offset
		// Acknowledge with AccAck
		ipod.Respond(req, tr, &AccAck{
			Status: ACKStatusSuccess,
			CmdID:  0x05, // SetVideoDelay command ID
		})

	default:
		_ = msg
	}
	return nil
}
