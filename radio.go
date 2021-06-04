package gomesh

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"math/rand"
	"strings"
	"time"

	"github.com/jacobsa/go-serial/serial"
	pb "github.com/lmatte7/gomesh/github.com/meshtastic/gomeshproto"
	"google.golang.org/protobuf/proto"
)

const start1 = byte(0x94)
const start2 = byte(0xc3)
const headerLen = 4
const maxToFromRadioSzie = 512
const broadcastAddr = "^all"
const localAddr = "^local"
const defaultHopLimit = 3
const broadcastNum = 0xffffffff

// Radio holds the port and serial io.ReadWriteCloser struct to maintain one serial connection
type Radio struct {
	portNumber string
	serialPort io.ReadWriteCloser
}

// Init initializes the Serial connection for the radio
func (r *Radio) Init(serialPort string) error {
	r.portNumber = serialPort
	//Configure the serial port
	options := serial.OpenOptions{
		PortName:              r.portNumber,
		BaudRate:              921600,
		DataBits:              8,
		StopBits:              1,
		MinimumReadSize:       0,
		InterCharacterTimeout: 100,
		ParityMode:            serial.PARITY_NONE,
	}

	// Open the port.
	port, err := serial.Open(options)
	if err != nil {
		return err
	}

	r.serialPort = port

	return nil
}

// sendPacket takes a protbuf packet, construct the appropriate header and sends it to the radio
func (r *Radio) sendPacket(protobufPacket []byte) (err error) {

	packageLength := len(string(protobufPacket))

	header := []byte{start1, start2, byte(packageLength>>8) & 0xff, byte(packageLength) & 0xff}

	radioPacket := append(header, protobufPacket...)
	_, err = r.serialPort.Write(radioPacket)
	if err != nil {
		return err
	}

	time.Sleep(100 * time.Millisecond)

	return

}

// readResponse reads any responses in the serial port, convert them to a FromRadio protobuf and return
func (r *Radio) readResponse() (FromRadioPackets []*pb.FromRadio, err error) {

	b := make([]byte, 1)

	emptyByte := make([]byte, 0)
	processedBytes := make([]byte, 0)
	emptyByteCounter := 0
	/************************************************************************************************
	* Process the returned data byte by byte until we have a valid command
	* Each command will come back with [START1, START2, PROTOBUF_PACKET]
	* where the protobuf packet is sent in binary. After reading START1 and START2
	* we use the next bytes to find the length of the packet.
	* After finding the length the looop continues to gather bytes until the length of the gathered
	* bytes is equal to the packet length plus the header
	 */
	for {
		_, err := r.serialPort.Read(b)
		if bytes.Compare(b, []byte("*")) == 0 {
			emptyByteCounter++
		}
		if err == io.EOF || emptyByteCounter > 10 {
			break
		} else if err != nil {
			return nil, err
		}

		if len(b) > 0 {

			pointer := len(processedBytes)

			processedBytes = append(processedBytes, b...)

			if pointer == 0 {
				if b[0] != start1 {
					processedBytes = emptyByte
				}
			} else if pointer == 1 {
				if b[0] != start2 {
					processedBytes = emptyByte
				}
			} else if pointer >= headerLen {
				packetLength := int((processedBytes[2] << 8) + processedBytes[3])

				if pointer == headerLen {
					if packetLength > maxToFromRadioSzie {
						processedBytes = emptyByte
					}
				}

				if len(processedBytes) != 0 && pointer+1 == packetLength+headerLen {
					fromRadio := pb.FromRadio{}
					if err := proto.Unmarshal(processedBytes[headerLen:], &fromRadio); err != nil {
						return nil, err
					}
					FromRadioPackets = append(FromRadioPackets, &fromRadio)
					processedBytes = emptyByte
				}
			}

		} else {
			break
		}

	}

	return FromRadioPackets, nil

}

// sendAdminPacket builds a admin message packet to send to the radio
func (r *Radio) createAdminPacket(nodeNum uint32, payload []byte) (packetOut []byte, err error) {

	radioMessage := pb.ToRadio{
		PayloadVariant: &pb.ToRadio_Packet{
			Packet: &pb.MeshPacket{
				To:      nodeNum,
				WantAck: true,
				PayloadVariant: &pb.MeshPacket_Decoded{
					Decoded: &pb.Data{
						Payload:      payload,
						Portnum:      pb.PortNum_ADMIN_APP,
						WantResponse: true,
					},
				},
			},
		},
	}

	packetOut, err = proto.Marshal(&radioMessage)
	if err != nil {
		return nil, err
	}

	return

}

// getNodeNum returns the current NodeNumber after querying the radio
func (r *Radio) getNodeNum() (nodeNum uint32, err error) {
	// Send first request for Radio and Node information
	nodeInfo := pb.ToRadio{PayloadVariant: &pb.ToRadio_WantConfigId{WantConfigId: 42}}

	out, err := proto.Marshal(&nodeInfo)
	if err != nil {
		return 0, err
	}

	r.sendPacket(out)

	radioResponses, err := r.readResponse()
	if err != nil {
		return 0, err
	}

	// Gather the Node number for channel settings requests
	nodeNum = 0
	for _, response := range radioResponses {
		if info, ok := response.GetPayloadVariant().(*pb.FromRadio_MyInfo); ok {
			nodeNum = info.MyInfo.MyNodeNum
		}
	}

	return
}

// GetRadioInfo retrieves information from the radio including config and adjacent Node information
func (r *Radio) GetRadioInfo() (radioResponses []*pb.FromRadio, err error) {
	// Send first request for Radio and Node information
	nodeInfo := pb.ToRadio{PayloadVariant: &pb.ToRadio_WantConfigId{WantConfigId: 42}}

	out, err := proto.Marshal(&nodeInfo)
	if err != nil {
		return nil, err
	}

	r.sendPacket(out)

	radioResponses, err = r.readResponse()
	if err != nil {
		return nil, err
	}

	// Gather the Node number for channel settings requests
	var nodeNum uint32
	nodeNum = 0
	for _, response := range radioResponses {
		if info, ok := response.GetPayloadVariant().(*pb.FromRadio_MyInfo); ok {
			nodeNum = info.MyInfo.MyNodeNum
		}
	}

	// Send a second request to retrieve channel information
	channelInfo := pb.AdminMessage{
		Variant: &pb.AdminMessage_GetChannelRequest{
			GetChannelRequest: 1,
		},
	}

	out, err = proto.Marshal(&channelInfo)
	if err != nil {
		return nil, err
	}

	packetOut, err := r.createAdminPacket(nodeNum, out)
	if err != nil {
		return nil, err
	}
	r.sendPacket(packetOut)

	newResponses, err := r.readResponse()
	if err != nil {
		return nil, err
	}

	radioResponses = append(radioResponses, newResponses...)

	return

}

func (r *Radio) GetChannelInfo(channel int) (channelSettings pb.AdminMessage, err error) {

	nodeNum, err := r.getNodeNum()

	channelInfo := pb.AdminMessage{
		Variant: &pb.AdminMessage_GetChannelRequest{
			GetChannelRequest: uint32(channel),
		},
	}

	out, err := proto.Marshal(&channelInfo)
	if err != nil {
		return pb.AdminMessage{}, err
	}

	if err != nil {
		return pb.AdminMessage{}, err
	}

	packetOut, err := r.createAdminPacket(nodeNum, out)
	if err != nil {
		return pb.AdminMessage{}, err
	}
	r.sendPacket(packetOut)

	channelResponses, err := r.readResponse()
	if err != nil {
		return pb.AdminMessage{}, err
	}

	var channelPacket []byte
	for _, response := range channelResponses {
		if packet, ok := response.GetPayloadVariant().(*pb.FromRadio_Packet); ok {
			if packet.Packet.GetDecoded().GetPortnum() == pb.PortNum_ADMIN_APP {
				channelPacket = packet.Packet.GetDecoded().GetPayload()
			}
		}
	}

	channelSettings = pb.AdminMessage{}

	if err := proto.Unmarshal(channelPacket, &channelSettings); err != nil {
		return pb.AdminMessage{}, err
	}

	return
}

// SendTextMessage sends a free form text message to other radios
func (r *Radio) SendTextMessage(message string, to int64) error {
	var address int64
	if to == 0 {
		address = broadcastNum
	} else {
		address = to
	}

	// This constant is defined in Constants_DATA_PAYLOAD_LEN, but not in a friendly way to use
	if len(message) > 240 {
		return errors.New("Message too large")
	}

	rand.Seed(time.Now().UnixNano())
	packetID := rand.Intn(2386828-1) + 1

	radioMessage := pb.ToRadio{
		PayloadVariant: &pb.ToRadio_Packet{
			Packet: &pb.MeshPacket{
				To:      uint32(address),
				WantAck: true,
				Id:      uint32(packetID),
				PayloadVariant: &pb.MeshPacket_Decoded{
					Decoded: &pb.Data{
						Payload: []byte(message),
						Portnum: pb.PortNum_TEXT_MESSAGE_APP,
					},
				},
			},
		},
	}

	out, err := proto.Marshal(&radioMessage)
	if err != nil {
		return err
	}

	if err := r.sendPacket(out); err != nil {
		return err
	}

	return nil

}

// SetRadioOwner sets the owner of the radio visible on the public mesh
func (r *Radio) SetRadioOwner(name string) error {

	if len(name) <= 2 {
		return errors.New("Name too short")
	}

	adminPacket := pb.AdminMessage{
		Variant: &pb.AdminMessage_SetOwner{
			SetOwner: &pb.User{
				LongName:  name,
				ShortName: name[:3],
			},
		},
	}

	out, err := proto.Marshal(&adminPacket)
	if err != nil {
		return err
	}

	nodeNum, err := r.getNodeNum()
	if err != nil {
		return err
	}
	packet, err := r.createAdminPacket(nodeNum, out)
	if err != nil {
		return err
	}

	if err := r.sendPacket(packet); err != nil {
		return err
	}

	return nil
}

// SetChannelURL sets the channel for the radio. The incoming channel should match the meshtastic URL format
// of a URL ending with /#{base_64_encoded_radio_params}
func (r *Radio) SetChannelURL(url string) error {

	// Split and unmarshel incoming base64 encoded protobuf packet
	split := strings.Split(url, "#")
	channel := split[len(split)-1]

	missing_padding := len(channel) % 4
	if missing_padding > 0 {
		channel += strings.Repeat("=", (4 - missing_padding))
	}

	cDec, err := base64.StdEncoding.DecodeString(channel)
	if err != nil {
		return err
	}
	encChannels := pb.ChannelSet{}

	if err := proto.Unmarshal(cDec, &encChannels); err != nil {
		return err
	}

	// The default pre-shared key
	// PskByte := []byte{0xd4, 0xf1, 0xbb, 0x3a, 0x20, 0x29, 0x07, 0x59, 0xf0, 0xbc, 0xff, 0xab, 0xcf, 0x4e, 0x69, 0xbf}

	var protoChannel *pb.ChannelSettings
	for i, cSet := range encChannels.Settings {
		protoChannel = cSet

		var role pb.Channel_Role
		if i == 0 {
			role = pb.Channel_PRIMARY
		} else {
			role = pb.Channel_SECONDARY
		}

		// Send settings to Radio
		adminPacket := pb.AdminMessage{
			Variant: &pb.AdminMessage_SetChannel{
				SetChannel: &pb.Channel{
					Index: int32(i),
					Role:  role,
					Settings: &pb.ChannelSettings{
						Psk:         protoChannel.Psk,
						ModemConfig: protoChannel.ModemConfig,
					},
				},
			},
		}

		out, err := proto.Marshal(&adminPacket)
		if err != nil {
			return err
		}

		nodeNum, err := r.getNodeNum()
		if err != nil {
			return err
		}
		packet, err := r.createAdminPacket(nodeNum, out)
		if err != nil {
			return err
		}

		if err := r.sendPacket(packet); err != nil {
			return err
		}
	}

	return nil
}

// SetModemMode sets the channel modem setting to be fast or slow
func (r *Radio) SetModemMode(channel int) error {

	var modemSetting int

	if channel == 0 {
		modemSetting = int(pb.ChannelSettings_Bw125Cr48Sf4096)
	} else {
		modemSetting = int(pb.ChannelSettings_Bw500Cr45Sf128)
	}

	chanSettings, err := r.GetChannelInfo(1)
	if err != nil {
		return err
	}

	// Send settings to Radio
	adminPacket := pb.AdminMessage{
		Variant: &pb.AdminMessage_SetChannel{
			SetChannel: &pb.Channel{
				Index: 0,
				Role:  chanSettings.GetGetChannelResponse().GetRole(),
				Settings: &pb.ChannelSettings{
					Psk:         chanSettings.GetGetChannelResponse().GetSettings().GetPsk(),
					ModemConfig: pb.ChannelSettings_ModemConfig(modemSetting),
				},
			},
		},
	}

	out, err := proto.Marshal(&adminPacket)
	if err != nil {
		return err
	}

	nodeNum, err := r.getNodeNum()
	if err != nil {
		return err
	}
	packet, err := r.createAdminPacket(nodeNum, out)
	if err != nil {
		return err
	}

	if err := r.sendPacket(packet); err != nil {
		return err
	}

	return nil

}

// SetChannel sets one of two channels for the radio
// func (r *Radio) SetChannel(channel int) error {

// 	var modemSetting int

// 	if channel == 0 {
// 		modemSetting = int(pb.ChannelSettings_Bw125Cr48Sf4096)
// 	} else {
// 		modemSetting = int(pb.ChannelSettings_Bw500Cr45Sf128)
// 	}

// 	responses, err := r.GetRadioInfo()

// 	if err != nil {
// 		fmt.Println(err)
// 	}

// 	var currentRadioInfo *pb.FromRadio_Radio

// 	for _, response := range responses {

// 		if radioInfo, ok := response.GetPayloadVariant().(*pb.FromRadio_Radio); ok {

// 			currentRadioInfo = radioInfo

// 		}

// 	}

// 	chSet := pb.ToRadio{
// 		PayloadVariant: &pb.ToRadio_SetChannel{
// 			SetChannel: &pb.ChannelSettings{
// 				Psk:         currentRadioInfo.Radio.ChannelSettings.Psk,
// 				ModemConfig: pb.ChannelSettings_ModemConfig(modemSetting),
// 			},
// 		},
// 	}

// 	out, err := proto.Marshal(&chSet)
// 	if err != nil {
// 		return err
// 	}

// 	if err := r.sendPacket(out); err != nil {
// 		return err
// 	}

// 	return nil

// }

// SetUserPreferences allows an freeform setting of values in the RadioConfig_UserPreferences struct
// func (r *Radio) SetUserPreferences(key string, value string) error {

// 	responses, err := r.GetRadioInfo()

// 	if err != nil {
// 		fmt.Println(err)
// 	}

// 	var currentRadioInfo *pb.FromRadio_Radio

// 	for _, response := range responses {

// 		if radioInfo, ok := response.GetPayloadVariant().(*pb.FromRadio_Radio); ok {

// 			currentRadioInfo = radioInfo

// 		}

// 	}

// 	rPref := reflect.ValueOf(currentRadioInfo.Radio.Preferences)

// 	rPref = rPref.Elem()

// 	fv := rPref.FieldByName(key)
// 	if !fv.IsValid() {
// 		return errors.New("Unknown Field")
// 	}

// 	if !fv.CanSet() {
// 		return errors.New("Unknown Field")
// 	}

// 	vType := fv.Type()

// 	// The acceptable values that can be set from the command line are uint32 and bool, so only check for those
// 	switch vType.Kind() {
// 	case reflect.Bool:
// 		boolValue, err := strconv.ParseBool(value)
// 		if err != nil {
// 			return err
// 		}
// 		fv.SetBool(boolValue)
// 		break
// 	case reflect.Uint32:
// 		uintValue, err := strconv.ParseUint(value, 10, 32)
// 		if err != nil {
// 			return err
// 		}
// 		fv.SetUint(uintValue)
// 	}

// 	prefSet := pb.ToRadio{
// 		PayloadVariant: &pb.ToRadio_SetRadio{
// 			SetRadio: &pb.RadioConfig{
// 				Preferences:     currentRadioInfo.Radio.Preferences,
// 				ChannelSettings: currentRadioInfo.Radio.ChannelSettings,
// 			},
// 		},
// 	}

// 	out, err := proto.Marshal(&prefSet)
// 	if err != nil {
// 		return err
// 	}

// 	if err := r.sendPacket(out); err != nil {
// 		return err
// 	}

// 	return nil
// }

// Close closes the serial port. Added so users can defer the close after opening
func (r *Radio) Close() {
	if r.serialPort != nil {
		r.serialPort.Close()
	}
}
