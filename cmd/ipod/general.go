package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/sirupsen/logrus"

	"github.com/oandrew/ipod"
	"github.com/oandrew/ipod/avrcp"
	audio "github.com/oandrew/ipod/lingo-audio"
	general "github.com/oandrew/ipod/lingo-general"
)

// Test device certificate (self-signed, for authentication testing)
const deviceCertBase64 = `
MIICXDCCAcWgAwIBAgIUH8O7wJbzGO4gPEXNv/Rc+lrC6q0wDQYJKoZIhvcNAQELBQAwQDELMAkG
A1UEBhMCVVMxDTALBgNVBAoMBFRlc3QxDTALBgNVBAsMBFRlc3QxEzARBgNVBAMMClRlc3REZXZp
Y2UwHhcNMjYwMjIxMTAyODEwWhcNMzYwMjE5MTAyODEwWjBAMQswCQYDVQQGEwJVUzENMAsGA1UE
CgwEVGVzdDENMAsGA1UECwwEVGVzdDETMBEGA1UEAwwKVGVzdERldmljZTCBnzANBgkqhkiG9w0B
AQEFAAOBjQAwgYkCgYEA6VPZQshKBig2C8qBxyaPoyX9KXYbVArdEUjY12Vr2J3RWiQoi5x44efZ
Y6fh5bGGKmXNhbrw6zjNAKNfbdq/GO+o5zZG7D656MTCk7UsTYxS97JcuIne3UKZAndIrXGFVuiV
HMDV/fxmtJcxRPW72ICCfJEcSuhVKjC+UxSfE18CAwEAAaNTMFEwHQYDVR0OBBYEFIB19OIAg3GK
srUdy0929KVbKv75MB8GA1UdIwQYMBaAFIB19OIAg3GKsrUdy0929KVbKv75MA8GA1UdEwEB/wQF
MAMBAf8wDQYJKoZIhvcNAQELBQADgYEAR0LO5dgv97R+1l01EmUCsGnXq+5GE4+8uK+77TBNDM8q
+QqSm+VflcqiC0Jz6mhLk46JEOOlvAAJWISI4AEGff5AoSgMtgJtrxNqSvIkXxTw8cgp/yICMjOy
HOAqPvXfMDvmtRwwRdeLvFcfo6cXa7cFi9gMdmhQs7N7w2hQB5o=
`

type DevGeneral struct {
	uimode        general.UIMode
	tokens        []general.FIDTokenValue
	cmdWriter     ipod.CommandWriter
	authChallenge [20]byte
	BtSource      *avrcp.Source // provides connected BT device name
}

var _ general.DeviceGeneral = &DevGeneral{}

func (d *DevGeneral) UIMode() general.UIMode {
	return d.uimode
}

func (d *DevGeneral) SetUIMode(mode general.UIMode) {
	d.uimode = mode
}

// btAliasFile is where the last confirmed-connected device alias is persisted
// across restarts.
const btAliasFile = "/var/lib/ipod-gadget/last_bt_alias"

// loadPersistedBTAlias reads the last stored alias from disk and seeds the
// in-memory cache. Called once during startup before the refresh loop.
func loadPersistedBTAlias() {
	data, err := os.ReadFile(btAliasFile)
	if err != nil {
		return // file may not exist yet – that's fine
	}
	alias := strings.TrimSpace(string(data))
	if alias != "" {
		btAlias.Lock()
		btAlias.value = alias
		btAlias.Unlock()
	}
}

// persistBTAlias writes alias to disk so it survives restarts.
func persistBTAlias(alias string) {
	if err := os.MkdirAll(filepath.Dir(btAliasFile), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(btAliasFile, []byte(alias+"\n"), 0o644)
}

// btAlias is a cache of the last known connected Bluetooth device alias.
var btAlias struct {
	sync.Mutex
	value string
}

// refreshBTAlias queries BlueZ for the connected device Alias and updates the
// cache. It is always called in a background goroutine so it never blocks the
// iAP frame read loop.
func refreshBTAlias() {
	out, err := exec.Command("busctl", "--system", "tree", "org.bluez").Output()
	if err != nil {
		return
	}
	var devicePaths []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimLeft(strings.TrimSpace(sc.Text()), "├─└│ ")
		if strings.Contains(line, "/dev_") && !strings.Contains(line, "/player") {
			devicePaths = append(devicePaths, line)
		}
	}

	type devProps struct {
		alias     string
		connected bool
	}
	getProps := func(path string) (devProps, bool) {
		res, err := exec.Command("busctl", "--system", "--json=short",
			"call", "org.bluez", path,
			"org.freedesktop.DBus.Properties", "GetAll",
			"s", "org.bluez.Device1").Output()
		if err != nil || len(res) == 0 {
			return devProps{}, false
		}
		var wrapper struct {
			Data []map[string]struct {
				Data json.RawMessage `json:"data"`
			} `json:"data"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(res), &wrapper); err != nil || len(wrapper.Data) == 0 {
			return devProps{}, false
		}
		props := wrapper.Data[0]
		var dp devProps
		if v, ok := props["Alias"]; ok {
			json.Unmarshal(v.Data, &dp.alias)
		}
		if v, ok := props["Connected"]; ok {
			json.Unmarshal(v.Data, &dp.connected)
		}
		return dp, dp.alias != ""
	}

	var fallback string
	for _, path := range devicePaths {
		dp, ok := getProps(path)
		if !ok {
			continue
		}
		if fallback == "" {
			fallback = dp.alias
		}
		if dp.connected {
			btAlias.Lock()
			btAlias.value = dp.alias
			btAlias.Unlock()
			persistBTAlias(dp.alias)
			return
		}
	}
	if fallback != "" {
		btAlias.Lock()
		btAlias.value = fallback
		btAlias.Unlock()
	}
}

// startBTAliasRefresher loads any persisted alias, then does an immediate async
// lookup and continues refreshing every 60s.
func startBTAliasRefresher() {
	loadPersistedBTAlias()
	go func() {
		refreshBTAlias()
		for range time.Tick(60 * time.Second) {
			refreshBTAlias()
		}
	}()
}

func (d *DevGeneral) Name() string {
	btAlias.Lock()
	v := btAlias.value
	btAlias.Unlock()
	if v == "" {
		return "ipod-gadget"
	}
	return v
}

func (d *DevGeneral) SoftwareVersion() (major uint8, minor uint8, rev uint8) {
	return 7, 1, 2
}

func (d *DevGeneral) SerialNum() string {
	return "abcd1234"
}

func (d *DevGeneral) LingoProtocolVersion(lingo uint8) (major uint8, minor uint8) {
	switch lingo {
	case ipod.LingoGeneralID:
		return 1, 9
	case ipod.LingoDisplayRemoteID:
		return 1, 5
	case ipod.LingoExtRemoteID:
		return 1, 12
	case ipod.LingoDigitalAudioID:
		return 1, 2
	default:
		return 1, 1
	}
}

func (d *DevGeneral) LingoOptions(lingo uint8) uint64 {
	switch lingo {
	case ipod.LingoGeneralID:
		return 0x000000063DEF73FF

	default:
		return 0
	}
}

func (d *DevGeneral) PrefSettingID(classID uint8) uint8 {
	return 0
}

func (d *DevGeneral) SetPrefSettingID(classID uint8, settingID uint8, restoreOnExit bool) {
}

func (d *DevGeneral) SetEventNotificationMask(mask uint64) {

}

func (d *DevGeneral) EventNotificationMask() uint64 {
	return 0
}

func (d *DevGeneral) SupportedEventNotificationMask() uint64 {
	return 0
}

func (d *DevGeneral) CancelCommand(lingo uint8, cmd uint16, transaction uint16) {

}

func (d *DevGeneral) MaxPayload() uint16 {
	return 65535
}

func (d *DevGeneral) StartIDPS() {
	d.tokens = make([]general.FIDTokenValue, 0)
}

func (d *DevGeneral) SetToken(token general.FIDTokenValue) error {
	d.tokens = append(d.tokens, token)
	return nil
}

func (d *DevGeneral) EndIDPS(status general.AccEndIDPSStatus) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Tokens:\n")
	for _, token := range d.tokens {

		fmt.Fprintf(&buf, "* Token: %T\n", token.Token)
		//log.Printf("New token: %T", token.Token)
		switch t := token.Token.(type) {
		case *general.FIDIdentifyToken:

		case *general.FIDAccCapsToken:
			for _, c := range general.AccCaps {
				if t.AccCapsBitmask&uint64(c) != 0 {
					fmt.Fprintf(&buf, "Capability: %v\n", c)
				}
			}
		case *general.FIDAccInfoToken:
			key := general.AccInfoType(t.AccInfoType).String()
			fmt.Fprintf(&buf, "%s: %s\n", key, spew.Sdump(t.Value))

		case *general.FIDiPodPreferenceToken:

		case *general.FIDEAProtocolToken:

		case *general.FIDBundleSeedIDPrefToken:

		case *general.FIDScreenInfoToken:

		case *general.FIDEAProtocolMetadataToken:

		case *general.FIDMicrophoneCapsToken:

		}

	}
	log.Print(buf.String())
}

func (d *DevGeneral) OnAuthenticationComplete() {
	log.WithField("module", "DevGeneral").Info("[AUTH] OnAuthenticationComplete() - authorization successful, starting audio")
	if d.cmdWriter != nil {
		audio.Start(d.cmdWriter)
		log.Info("[AUDIO] Audio started")
	} else {
		log.Warn("[AUTH] No command writer available for audio startup")
	}
}

func (d *DevGeneral) GenerateAuthChallenge() [20]byte {
	rand.Read(d.authChallenge[:])
	return d.authChallenge
}

func (d *DevGeneral) GetStoredChallenge() [20]byte {
	return d.authChallenge
}

func (d *DevGeneral) StoreAuthChallenge(challenge [20]byte) {
	d.authChallenge = challenge
	log.WithField("challenge", fmt.Sprintf("%02x", challenge[:])).Debug("[AUTH] Challenge stored in device")
}

func (d *DevGeneral) GetDeviceAuthenticationInfo() (major uint8, minor uint8, certData []byte) {
	// Decode test device certificate
	decodedCert, err := base64.StdEncoding.DecodeString(deviceCertBase64)
	if err != nil {
		log.WithError(err).Warn("[AUTH] Failed to decode device certificate")
		return 2, 0, nil
	}

	log.WithFields(logrus.Fields{
		"major":     uint8(2),
		"minor":     uint8(0),
		"cert_size": len(decodedCert),
	}).Infof("[AUTH] Returning device certificate (test certificate, %d bytes)", len(decodedCert))
	return 2, 0, decodedCert
}
