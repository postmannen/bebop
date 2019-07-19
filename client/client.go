package client

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"time"
)

func validatePitch(val int) int {
	if val > 100 {
		return 100
	} else if val < 0 {
		return 0
	}

	return val
}

type tmpFrame struct {
	arstreamACK   ARStreamACK
	fragments     map[int][]byte
	frame         []byte
	waitForIframe bool
	frameFlags    int
}

//ARStreamACK ARSTreamACK
type ARStreamACK struct {
	FrameNumber    int
	HighPacketsAck uint64
	LowPacketsAck  uint64
}

//ARStreamFrame ARStreamFrame
type ARStreamFrame struct {
	FrameNumber       int
	FrameFlags        int
	FragmentNumber    int
	FragmentsPerFrame int
	Frame             []byte
}

//NewARStreamFrame NewARStreamFrame
func NewARStreamFrame(buf []byte) ARStreamFrame {
	//
	// ARSTREAM_NetworkHeaders_DataHeader_t;
	//
	// uint16_t frameNumber;
	// uint8_t  frameFlags; // Infos on the current frame
	// uint8_t  fragmentNumber; // Index of the current fragment in current frame
	// uint8_t  fragmentsPerFrame; // Number of fragments in current frame
	//
	// * frameFlags structure :
	// *  x x x x x x x x
	// *  | | | | | | | \-> FLUSH FRAME
	// *  | | | | | | \-> UNUSED
	// *  | | | | | \-> UNUSED
	// *  | | | | \-> UNUSED
	// *  | | | \-> UNUSED
	// *  | | \-> UNUSED
	// *  | \-> UNUSED
	// *  \-> UNUSED
	// *
	//

	frame := ARStreamFrame{
		FrameFlags:        int(buf[2]),
		FragmentNumber:    int(buf[3]),
		FragmentsPerFrame: int(buf[4]),
	}

	var number uint16
	err := binary.Read(bytes.NewReader(buf[0:2]), binary.LittleEndian, &number)
	if err != nil {
		log.Println("error: NewARStreamFrame, binary.Read: ", err)
	}

	frame.FrameNumber = int(number)

	frame.Frame = buf[5:]

	return frame
}

//NetworkFrame NetworkFrame
type NetworkFrame struct {
	Type int
	Seq  int
	ID   int
	Size int
	Data []byte
}

//NewNetworkFrame NewNetworkFrame
func NewNetworkFrame(buf []byte) NetworkFrame {
	frame := NetworkFrame{
		Type: int(buf[0]),
		ID:   int(buf[1]),
		Seq:  int(buf[2]),
		Data: []byte{},
	}

	var size uint32
	err := binary.Read(bytes.NewReader(buf[3:7]), binary.LittleEndian, &size)
	if err != nil {
		log.Println("error: NewNetworkFrame, binary.Read: ", err)
	}
	frame.Size = int(size)

	frame.Data = buf[7:frame.Size]

	return frame
}

func networkFrameGenerator() func(*bytes.Buffer, byte, byte) *bytes.Buffer {
	//func networkFrameGenerator() func(*bytes.Buffer, byte, byte) NetworkFrame {
	//
	// ARNETWORKAL_Frame_t
	//
	// uint8  type  - frame type ARNETWORK_FRAME_TYPE
	// uint8  id    - identifier of the buffer sending the frame
	// uint8  seq   - sequence number of the frame
	// uint32 size  - size of the frame
	//

	// each frame id has it's own sequence number
	seq := make(map[byte]byte)

	hlen := 7 // size of ARNETWORKAL_Frame_t header

	return func(cmd *bytes.Buffer, frameType byte, id byte) *bytes.Buffer {
		if _, ok := seq[id]; !ok {
			seq[id] = 0
		}

		seq[id]++

		if seq[id] > 255 {
			seq[id] = 0
		}

		ret := &bytes.Buffer{}
		ret.WriteByte(frameType)
		ret.WriteByte(id)
		ret.WriteByte(seq[id])

		size := &bytes.Buffer{}
		err := binary.Write(size, binary.LittleEndian, uint32(cmd.Len()+hlen))
		if err != nil {
			log.Println("error: networkFrameGenerator, binary.Read: ", err)
		}

		ret.Write(size.Bytes())
		ret.Write(cmd.Bytes())

		return ret
	}
}

//Pcmd Pcmd
type Pcmd struct {
	Flag  int
	Roll  int
	Pitch int
	Yaw   int
	Gaz   int
	Psi   float32
}

//Bebop Bebop
type Bebop struct {
	IP                    string
	NavData               map[string]string
	Pcmd                  Pcmd
	tmpFrame              tmpFrame
	C2dPort               int
	D2cPort               int
	RTPStreamPort         int
	RTPControlPort        int
	DiscoveryPort         int
	c2dClient             *net.UDPConn
	d2cClient             *net.UDPConn
	discoveryClient       *net.TCPConn
	networkFrameGenerator func(*bytes.Buffer, byte, byte) *bytes.Buffer
	video                 chan []byte
	writeChan             chan []byte
}

//New will return a *Bebop, which is a struct containing things to control the drone like ports to use,
// ip address, networkFrameGenerator(), and so on
func New() *Bebop {
	return &Bebop{
		IP:                    "192.168.42.1",
		NavData:               make(map[string]string),
		C2dPort:               54321,
		D2cPort:               43210,
		RTPStreamPort:         55004,
		RTPControlPort:        55005,
		DiscoveryPort:         44444,
		networkFrameGenerator: networkFrameGenerator(),
		Pcmd: Pcmd{
			Flag:  0,
			Roll:  0,
			Pitch: 0,
			Yaw:   0,
			Gaz:   0,
			Psi:   0,
		},
		tmpFrame:  tmpFrame{},
		video:     make(chan []byte),
		writeChan: make(chan []byte),
	}
}

func (b *Bebop) write(buf []byte) (int, error) {
	b.writeChan <- buf
	return 0, nil
}

//Discover Discover
func (b *Bebop) Discover() error {
	addr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("%s:%d", b.IP, b.DiscoveryPort))

	if err != nil {
		return err
	}

	b.discoveryClient, err = net.DialTCP("tcp", nil, addr)

	if err != nil {
		return err
	}

	_, err = b.discoveryClient.Write(
		[]byte(
			fmt.Sprintf(`{
						"controller_type": "computer",
						"controller_name": "go-bebop",
						"d2c_port": "%d",
						"arstream2_client_stream_port": "%d",
						"arstream2_client_control_port": "%d",
						}`,
				b.D2cPort,
				b.RTPStreamPort,
				b.RTPControlPort),
		),
	)
	if err != nil {
		log.Println("error: Discover, discoveryClient.Write: ", err)
	}

	data := make([]byte, 10240)

	_, err = b.discoveryClient.Read(data)

	if err != nil {
		return err
	}

	return b.discoveryClient.Close()
}

//Connect Connect
func (b *Bebop) Connect() error {
	err := b.Discover()

	if err != nil {
		return err
	}

	c2daddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", b.IP, b.C2dPort))

	if err != nil {
		return err
	}

	//Will start an UDP connection from the controller to the drone (c2d). The session will be stored at *Bebop.c2dClient
	b.c2dClient, err = net.DialUDP("udp", nil, c2daddr)

	if err != nil {
		return err
	}

	d2caddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", b.D2cPort))

	if err != nil {
		return err
	}

	//Will start and UDP listener on the controller for drone->controller traffic (d2c), the connection is stored in the Bebop struct as Bebop.d2cClient
	b.d2cClient, err = net.ListenUDP("udp", d2caddr)
	if err != nil {
		return err
	}

	//Start a Go Routine who will block on *Bebop.writeChan, and send any messages received to the drone.
	go func() {
		for {
			_, err := b.c2dClient.Write(<-b.writeChan)

			if err != nil {
				fmt.Println(err)
			}
		}
	}()

	//Start a Go Routine who will constantly check for new UDP packets on the listening UDP port,
	// and handle them with the *Bebop.packetReceiver() function.
	go func() {
		for {
			data := make([]byte, 40960)
			i, _, err := b.d2cClient.ReadFromUDP(data)
			if err != nil {
				fmt.Println("d2cClient error:", err)
			}

			b.packetReceiver(data[0:i])
		}
	}()

	// send pcmd values at 40hz
	go func() {
		// wait a little bit so that there is enough time to get some ACKs
		time.Sleep(500 * time.Millisecond)
		for {
			_, err := b.write(b.generatePcmd().Bytes())
			if err != nil {
				fmt.Println("pcmd c2dClient.Write", err)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()

	if err := b.GenerateAllStates(); err != nil {
		return err
	}
	if err := b.FlatTrim(); err != nil {
		return err
	}

	return nil
}

//FlatTrim do
func (b *Bebop) FlatTrim() error {
	//
	// ARCOMMANDS_Generator_GenerateARDrone3PilotingFlatTrim
	//

	cmd := &bytes.Buffer{}

	cmd.WriteByte(ARCOMMANDS_ID_PROJECT_ARDRONE3)
	cmd.WriteByte(ARCOMMANDS_ID_ARDRONE3_CLASS_PILOTING)

	tmp := &bytes.Buffer{}
	err := binary.Write(tmp, binary.LittleEndian, uint16(ARCOMMANDS_ID_ARDRONE3_PILOTING_CMD_FLATTRIM))
	if err != nil {
		log.Println("error: FlatTrim, binary.Read: ", err)
	}

	cmd.Write(tmp.Bytes())

	_, err = b.write(b.networkFrameGenerator(cmd, ARNETWORKAL_FRAME_TYPE_DATA, BD_NET_CD_NONACK_ID).Bytes())
	return err
}

//GenerateAllStates do
func (b *Bebop) GenerateAllStates() error {
	//
	// ARCOMMANDS_Generator_GenerateCommonCommonAllStates
	//

	cmd := &bytes.Buffer{}

	cmd.WriteByte(ARCOMMANDS_ID_PROJECT_COMMON)
	cmd.WriteByte(ARCOMMANDS_ID_COMMON_CLASS_COMMON)

	tmp := &bytes.Buffer{}
	err := binary.Write(tmp, binary.LittleEndian, uint16(ARCOMMANDS_ID_COMMON_COMMON_CMD_ALLSTATES))
	if err != nil {
		log.Println("error: GenerateAllStates, binary.Read: ", err)
	}

	cmd.Write(tmp.Bytes())

	_, err = b.write(b.networkFrameGenerator(cmd, ARNETWORKAL_FRAME_TYPE_DATA, BD_NET_CD_NONACK_ID).Bytes())
	return err
}

//TakeOff do
func (b *Bebop) TakeOff() error {
	//
	//  ARCOMMANDS_Generator_GenerateARDrone3PilotingTakeOff
	//

	cmd := &bytes.Buffer{}

	cmd.WriteByte(ARCOMMANDS_ID_PROJECT_ARDRONE3)
	cmd.WriteByte(ARCOMMANDS_ID_ARDRONE3_CLASS_PILOTING)

	tmp := &bytes.Buffer{}
	err := binary.Write(tmp, binary.LittleEndian, uint16(ARCOMMANDS_ID_ARDRONE3_PILOTING_CMD_TAKEOFF))
	if err != nil {
		log.Println("error: TakeOff, binary.Read: ", err)
	}

	cmd.Write(tmp.Bytes())

	_, err = b.write(b.networkFrameGenerator(cmd, ARNETWORKAL_FRAME_TYPE_DATA, BD_NET_CD_NONACK_ID).Bytes())
	return err
}

//Land do
func (b *Bebop) Land() error {
	//
	// ARCOMMANDS_Generator_GenerateARDrone3PilotingLanding
	//

	cmd := &bytes.Buffer{}

	cmd.WriteByte(ARCOMMANDS_ID_PROJECT_ARDRONE3)
	cmd.WriteByte(ARCOMMANDS_ID_ARDRONE3_CLASS_PILOTING)

	tmp := &bytes.Buffer{}
	err := binary.Write(tmp, binary.LittleEndian, uint16(ARCOMMANDS_ID_ARDRONE3_PILOTING_CMD_LANDING))
	if err != nil {
		log.Println("error: Land, binary.Read: ", err)
	}

	cmd.Write(tmp.Bytes())

	_, err = b.write(b.networkFrameGenerator(cmd, ARNETWORKAL_FRAME_TYPE_DATA, BD_NET_CD_NONACK_ID).Bytes())
	return err
}

//Up do
func (b *Bebop) Up(val int) error {
	b.Pcmd.Flag = 1
	b.Pcmd.Gaz = validatePitch(val)
	return nil
}

//Down do
func (b *Bebop) Down(val int) error {
	b.Pcmd.Flag = 1
	b.Pcmd.Gaz = validatePitch(val) * -1
	return nil
}

//Forward do
func (b *Bebop) Forward(val int) error {
	b.Pcmd.Flag = 1
	b.Pcmd.Pitch = validatePitch(val)
	return nil
}

//Backward do
func (b *Bebop) Backward(val int) error {
	b.Pcmd.Flag = 1
	b.Pcmd.Pitch = validatePitch(val) * -1
	return nil
}

//Right do
func (b *Bebop) Right(val int) error {
	b.Pcmd.Flag = 1
	b.Pcmd.Roll = validatePitch(val)
	return nil
}

//Left do
func (b *Bebop) Left(val int) error {
	b.Pcmd.Flag = 1
	b.Pcmd.Roll = validatePitch(val) * -1
	return nil
}

//Clockwise do
func (b *Bebop) Clockwise(val int) error {
	b.Pcmd.Flag = 1
	b.Pcmd.Yaw = validatePitch(val)
	return nil
}

//CounterClockwise do
func (b *Bebop) CounterClockwise(val int) error {
	b.Pcmd.Flag = 1
	b.Pcmd.Yaw = validatePitch(val) * -1
	return nil
}

//Stop do
func (b *Bebop) Stop() error {
	b.Pcmd = Pcmd{
		Flag:  0,
		Roll:  0,
		Pitch: 0,
		Yaw:   0,
		Gaz:   0,
		Psi:   0,
	}

	return nil
}

func (b *Bebop) generatePcmd() *bytes.Buffer {
	//
	// ARCOMMANDS_Generator_GenerateARDrone3PilotingPCMD
	//
	// uint8 - flag Boolean flag to activate roll/pitch movement
	// int8  - roll Roll consign for the drone [-100;100]
	// int8  - pitch Pitch consign for the drone [-100;100]
	// int8  - yaw Yaw consign for the drone [-100;100]
	// int8  - gaz Gaz consign for the drone [-100;100]
	// float - psi [NOT USED] - Magnetic north heading of the
	//         controlling device (deg) [-180;180]
	//

	cmd := &bytes.Buffer{}
	tmp := &bytes.Buffer{}
	var err error

	cmd.WriteByte(ARCOMMANDS_ID_PROJECT_ARDRONE3)
	cmd.WriteByte(ARCOMMANDS_ID_ARDRONE3_CLASS_PILOTING)

	tmp = &bytes.Buffer{}
	err = binary.Write(tmp, binary.LittleEndian, uint16(ARCOMMANDS_ID_ARDRONE3_PILOTING_CMD_PCMD))
	if err != nil {
		log.Println("error: generatePcmd, binary.Read: ", err)
	}
	cmd.Write(tmp.Bytes())

	tmp = &bytes.Buffer{}
	err = binary.Write(tmp, binary.LittleEndian, uint8(b.Pcmd.Flag))
	if err != nil {
		log.Println("error: generatePcmd, binary.Read: ", err)
	}
	cmd.Write(tmp.Bytes())

	tmp = &bytes.Buffer{}
	err = binary.Write(tmp, binary.LittleEndian, int8(b.Pcmd.Roll))
	if err != nil {
		log.Println("error: generatePcmd, binary.Read: ", err)
	}
	cmd.Write(tmp.Bytes())

	tmp = &bytes.Buffer{}
	err = binary.Write(tmp, binary.LittleEndian, int8(b.Pcmd.Pitch))
	if err != nil {
		log.Println("error: generatePcmd, binary.Read: ", err)
	}
	cmd.Write(tmp.Bytes())

	tmp = &bytes.Buffer{}
	err = binary.Write(tmp, binary.LittleEndian, int8(b.Pcmd.Yaw))
	if err != nil {
		log.Println("error: generatePcmd, binary.Read: ", err)
	}
	cmd.Write(tmp.Bytes())

	tmp = &bytes.Buffer{}
	err = binary.Write(tmp, binary.LittleEndian, int8(b.Pcmd.Gaz))
	if err != nil {
		log.Println("error: generatePcmd, binary.Read: ", err)
	}
	cmd.Write(tmp.Bytes())

	tmp = &bytes.Buffer{}
	err = binary.Write(tmp, binary.LittleEndian, uint32(b.Pcmd.Psi))
	if err != nil {
		log.Println("error: generatePcmd, binary.Read: ", err)
	}
	cmd.Write(tmp.Bytes())

	return b.networkFrameGenerator(cmd, ARNETWORKAL_FRAME_TYPE_DATA, BD_NET_CD_NONACK_ID)
}

func (b *Bebop) createAck(frame NetworkFrame) *bytes.Buffer {
	//
	// ARNETWORK_Receiver_ThreadRun
	//

	//
	// libARNetwork/Sources/ARNETWORK_Manager.h#ARNETWORK_Manager_IDOutputToIDAck
	//

	return b.networkFrameGenerator(bytes.NewBuffer([]byte{uint8(frame.Seq)}),
		ARNETWORKAL_FRAME_TYPE_ACK,
		byte(uint16(frame.ID)+(ARNETWORKAL_MANAGER_DEFAULT_ID_MAX/2)),
	)
}

func (b *Bebop) createPong(frame NetworkFrame) *bytes.Buffer {
	return b.networkFrameGenerator(bytes.NewBuffer(frame.Data),
		ARNETWORKAL_FRAME_TYPE_DATA,
		ARNETWORK_MANAGER_INTERNAL_BUFFER_ID_PONG,
	)
}

func (b *Bebop) packetReceiver(buf []byte) {
	frame := NewNetworkFrame(buf)

	// =================TESTING=========================
	if frame.Type == int(ARCOMMANDS_ID_ARDRONE3_PILOTINGSTATE_CMD_POSITIONCHANGED) {
		//fmt.Println("FRAME EVENT Type:", frame.Type,
		//      "Id:", frame.Id, "size:", frame.Size, "data:", frame.Data)
		//fmt.Println("FRAME EVENT RAW:", frame)

		if len(frame.Data) != 0 {

			//The first 4 bytes of the data frame tells class and cmdID that
			//follows in the rest of the data frame.
			cmdProject := uint8(frame.Data[0])
			cmdClass := uint8(frame.Data[1])
			cmdID := int(frame.Data[2]) + int(frame.Data[3])
			cmdRemaining := frame.Data[4:]

			if cmdClass == 4 {
				fmt.Println("TEST -------------------------------------------------------------")
				fmt.Println("TEST RAW:", frame)
				fmt.Printf("TEST cmdProject: %v\n", cmdProject)
				fmt.Printf("TEST cmdClass: %v\n", cmdClass)
				fmt.Printf("TEST cmdID: %v\n", cmdID)
				fmt.Printf("TEST cmdRemaining: %v\n", cmdRemaining)

				//<cmd name="moveToChanged" id="12">, which is a total of 40 bytes
				if cmdID == 12 {
					latitude := cmdRemaining[0:8]          //double
					longitude := cmdRemaining[8:16]        //double
					altitude := cmdRemaining[16:24]        //double
					orientationMode := cmdRemaining[24:28] //enum (int32)
					heading := cmdRemaining[28:32]         //float
					status := cmdRemaining[32:36]          //enum (int32)

					fmt.Printf("lat:%v ,lng:%v ,alt:%v ,orientationMode:%v ,heading:%v, status:%v\n",
						latitude, longitude, altitude, orientationMode, heading, status)

					fmt.Printf("lat:%v ,lng:%v ,alt:%v ,orientationMode:%v ,heading:%v, status:%v\n",
						binary.LittleEndian.Uint64(latitude),
						binary.LittleEndian.Uint64(longitude),
						binary.LittleEndian.Uint64(altitude),
						binary.LittleEndian.Uint32(orientationMode),
						binary.LittleEndian.Uint32(heading),
						binary.LittleEndian.Uint32(status))
				}
			}

		}

	}
	// =================================================

	//
	// libARNetwork/Sources/ARNETWORK_Receiver.c#ARNETWORK_Receiver_ThreadRun
	//
	if frame.Type == int(ARNETWORKAL_FRAME_TYPE_DATA_WITH_ACK) {
		ack := b.createAck(frame).Bytes()
		_, err := b.write(ack)

		if err != nil {
			fmt.Println("ARNETWORKAL_FRAME_TYPE_DATA_WITH_ACK", err)
		}
	}

	if frame.Type == int(ARNETWORKAL_FRAME_TYPE_DATA_LOW_LATENCY) &&
		frame.ID == int(BD_NET_DC_VIDEO_DATA_ID) {

		arstreamFrame := NewARStreamFrame(frame.Data)

		ack := b.createARStreamACK(arstreamFrame).Bytes()
		_, err := b.write(ack)
		if err != nil {
			fmt.Println("ARNETWORKAL_FRAME_TYPE_DATA_LOW_LATENCY", err)
		}
	}

	//
	// libARNetwork/Sources/ARNETWORK_Receiver.c#ARNETWORK_Receiver_ThreadRun
	//
	if frame.ID == int(ARNETWORK_MANAGER_INTERNAL_BUFFER_ID_PING) {
		pong := b.createPong(frame).Bytes()
		_, err := b.write(pong)
		if err != nil {
			fmt.Println("ARNETWORK_MANAGER_INTERNAL_BUFFER_ID_PING", err)
		}
	}
}

//StartRecording do
func (b *Bebop) StartRecording() error {
	buf := b.videoRecord(ARCOMMANDS_ARDRONE3_MEDIARECORD_VIDEO_RECORD_START)

	_, err := b.write(b.networkFrameGenerator(buf, ARNETWORKAL_FRAME_TYPE_DATA, BD_NET_CD_NONACK_ID).Bytes())
	if err != nil {
		log.Println("error: StartRecording, *Bebop.write: ", err)
	}
	return nil
}

//StopRecording do
func (b *Bebop) StopRecording() error {
	buf := b.videoRecord(ARCOMMANDS_ARDRONE3_MEDIARECORD_VIDEO_RECORD_STOP)

	_, err := b.write(b.networkFrameGenerator(buf, ARNETWORKAL_FRAME_TYPE_DATA, BD_NET_CD_NONACK_ID).Bytes())
	if err != nil {
		log.Println("error: StopRecording, *Bebop.write: ", err)
	}
	return nil
}

func (b *Bebop) videoRecord(state byte) *bytes.Buffer {
	//
	// ARCOMMANDS_Generator_GenerateARDrone3MediaRecordVideo
	//

	cmd := &bytes.Buffer{}

	cmd.WriteByte(ARCOMMANDS_ID_PROJECT_ARDRONE3)
	cmd.WriteByte(ARCOMMANDS_ID_ARDRONE3_CLASS_MEDIARECORD)

	tmp := &bytes.Buffer{}
	err := binary.Write(tmp, binary.LittleEndian, uint16(ARCOMMANDS_ID_ARDRONE3_MEDIARECORD_CMD_VIDEO))
	if err != nil {
		log.Println("error: videoRecord, binary.Read: ", err)
	}
	cmd.Write(tmp.Bytes())

	tmp = &bytes.Buffer{}
	err = binary.Write(tmp, binary.LittleEndian, uint32(state))
	if err != nil {
		log.Println("error: videoRecord, binary.Read: ", err)
	}

	cmd.Write(tmp.Bytes())

	cmd.WriteByte(0)

	return cmd
}

//Video do
func (b *Bebop) Video() chan []byte {
	return b.video
}

//HullProtection do
func (b *Bebop) HullProtection(protect bool) error {
	//
	// ARCOMMANDS_Generator_GenerateARDrone3SpeedSettingsHullProtection
	//

	cmd := &bytes.Buffer{}

	cmd.WriteByte(ARCOMMANDS_ID_PROJECT_ARDRONE3)
	cmd.WriteByte(ARCOMMANDS_ID_ARDRONE3_CLASS_SPEEDSETTINGS)

	tmp := &bytes.Buffer{}
	err := binary.Write(tmp, binary.LittleEndian, uint16(ARCOMMANDS_ID_ARDRONE3_SPEEDSETTINGS_CMD_HULLPROTECTION))
	if err != nil {
		log.Println("error: Video, binary.Read: ", err)
	}
	cmd.Write(tmp.Bytes())

	tmp = &bytes.Buffer{}
	err = binary.Write(tmp, binary.LittleEndian, bool2int8(protect))
	if err != nil {
		log.Println("error: Video, binary.Read: ", err)
	}
	cmd.Write(tmp.Bytes())

	_, err = b.write(b.networkFrameGenerator(cmd, ARNETWORKAL_FRAME_TYPE_DATA, BD_NET_CD_NONACK_ID).Bytes())
	return err
}

//Outdoor do
func (b *Bebop) Outdoor(outdoor bool) error {
	//
	// ARCOMMANDS_Generator_GenerateARDrone3SpeedSettingsOutdoor
	//

	cmd := &bytes.Buffer{}

	cmd.WriteByte(ARCOMMANDS_ID_PROJECT_ARDRONE3)
	cmd.WriteByte(ARCOMMANDS_ID_ARDRONE3_CLASS_SPEEDSETTINGS)

	tmp := &bytes.Buffer{}
	err := binary.Write(tmp,
		binary.LittleEndian,
		uint16(ARCOMMANDS_ID_ARDRONE3_SPEEDSETTINGS_CMD_OUTDOOR),
	)
	if err != nil {
		log.Println("error: Outdoor, binary.Write: ", err)
	}

	cmd.Write(tmp.Bytes())

	tmp = &bytes.Buffer{}
	err = binary.Write(tmp, binary.LittleEndian, bool2int8(outdoor))
	if err != nil {
		log.Println("error: Outdoor, binary.Write: ", err)
	}
	cmd.Write(tmp.Bytes())

	_, err = b.write(b.networkFrameGenerator(cmd, ARNETWORKAL_FRAME_TYPE_DATA, BD_NET_CD_NONACK_ID).Bytes())
	return err
}

//VideoEnable do
func (b *Bebop) VideoEnable(enable bool) error {
	cmd := &bytes.Buffer{}

	cmd.WriteByte(ARCOMMANDS_ID_PROJECT_ARDRONE3)
	cmd.WriteByte(ARCOMMANDS_ID_ARDRONE3_CLASS_MEDIASTREAMING)

	tmp := &bytes.Buffer{}
	err := binary.Write(tmp,
		binary.LittleEndian,
		uint16(ARCOMMANDS_ID_ARDRONE3_MEDIASTREAMING_CMD_VIDEOENABLE),
	)
	if err != nil {
		log.Println("error: VideoEnable, binary.Write: ", err)
	}

	cmd.Write(tmp.Bytes())

	tmp = &bytes.Buffer{}
	err = binary.Write(tmp, binary.LittleEndian, bool2int8(enable))
	if err != nil {
		log.Println("error: VideoEnable, binary.Write: ", err)
	}
	cmd.Write(tmp.Bytes())

	_, err = b.write(b.networkFrameGenerator(cmd, ARNETWORKAL_FRAME_TYPE_DATA, BD_NET_CD_NONACK_ID).Bytes())
	return err
}

//VideoStreamMode do
func (b *Bebop) VideoStreamMode(mode int8) error {
	cmd := &bytes.Buffer{}

	cmd.WriteByte(ARCOMMANDS_ID_PROJECT_ARDRONE3)
	cmd.WriteByte(ARCOMMANDS_ID_ARDRONE3_CLASS_MEDIASTREAMING)

	tmp := &bytes.Buffer{}
	err := binary.Write(tmp,
		binary.LittleEndian,
		uint16(ARCOMMANDS_ID_ARDRONE3_MEDIASTREAMING_CMD_VIDEOSTREAMMODE),
	)
	if err != nil {
		log.Println("error: VideoStreamMode, binary.Write: ", err)
	}

	cmd.Write(tmp.Bytes())

	tmp = &bytes.Buffer{}
	err = binary.Write(tmp, binary.LittleEndian, mode)
	if err != nil {
		log.Println("error: VideoStreamMode, binary.Write: ", err)
	}
	cmd.Write(tmp.Bytes())

	_, err = b.write(b.networkFrameGenerator(cmd, ARNETWORKAL_FRAME_TYPE_DATA, BD_NET_CD_NONACK_ID).Bytes())
	return err
}

func bool2int8(b bool) int8 {
	if b {
		return 1
	}
	return 0
}

func (b *Bebop) createARStreamACK(frame ARStreamFrame) *bytes.Buffer {
	//
	// ARSTREAM_NetworkHeaders_AckPacket_t;
	//
	// uint16_t frameNumber;    // id of the current frame
	// uint64_t highPacketsAck; // Upper 64 packets bitfield
	// uint64_t lowPacketsAck;  // Lower 64 packets bitfield
	//
	// libARStream/Sources/ARSTREAM_NetworkHeaders.c#ARSTREAM_NetworkHeaders_AckPacketSetFlag
	//

	//
	// each bit in the highPacketsAck and lowPacketsAck correspond to the
	// fragmentsPerFrame which have been received per frameNumber, so time to
	// flip some bits!
	//
	if frame.FrameNumber != b.tmpFrame.arstreamACK.FrameNumber {
		if len(b.tmpFrame.fragments) > 0 {
			emit := false

			// if we missed some frames, wait for the next iframe
			if frame.FrameNumber != b.tmpFrame.arstreamACK.FrameNumber+1 {
				b.tmpFrame.waitForIframe = true
			}

			// if it's an iframe
			if b.tmpFrame.frameFlags == 1 {
				b.tmpFrame.waitForIframe = false
				emit = true
			} else if !b.tmpFrame.waitForIframe {
				emit = true
			}

			if emit {
				skip := false

				for i := 0; i < len(b.tmpFrame.fragments); i++ {
					// check if any fragments are missing
					if len(b.tmpFrame.fragments[i]) == 0 {
						skip = true
						break
					}
					b.tmpFrame.frame = append(b.tmpFrame.frame, b.tmpFrame.fragments[i]...)
				}

				if !skip {
					select {
					case b.video <- b.tmpFrame.frame:
					default:
					}
				}
			}
		}

		b.tmpFrame.fragments = make(map[int][]byte)
		b.tmpFrame.frame = []byte{}
		b.tmpFrame.arstreamACK.FrameNumber = frame.FrameNumber
		b.tmpFrame.frameFlags = frame.FrameFlags
	}
	b.tmpFrame.fragments[frame.FragmentNumber] = frame.Frame

	if frame.FragmentNumber < 64 {
		b.tmpFrame.arstreamACK.LowPacketsAck |= uint64(1) << uint64(frame.FragmentNumber)
	} else {
		b.tmpFrame.arstreamACK.HighPacketsAck |= uint64(1) << uint64(frame.FragmentNumber-64)
	}

	ackPacket := &bytes.Buffer{}
	tmp := &bytes.Buffer{}

	err := binary.Write(tmp, binary.LittleEndian, uint16(b.tmpFrame.arstreamACK.FrameNumber))
	if err != nil {
		log.Println("error: createARStreamACK, binary.Write: ", err)
	}
	ackPacket.Write(tmp.Bytes())

	tmp = &bytes.Buffer{}
	err = binary.Write(tmp, binary.LittleEndian, uint64(b.tmpFrame.arstreamACK.HighPacketsAck))
	if err != nil {
		log.Println("error: createARStreamACK, binary.Write: ", err)
	}
	ackPacket.Write(tmp.Bytes())

	tmp = &bytes.Buffer{}
	err = binary.Write(tmp, binary.LittleEndian, uint64(b.tmpFrame.arstreamACK.LowPacketsAck))
	if err != nil {
		log.Println("error: createARStreamACK, binary.Write: ", err)
	}
	ackPacket.Write(tmp.Bytes())

	return b.networkFrameGenerator(ackPacket, ARNETWORKAL_FRAME_TYPE_DATA, BD_NET_CD_VIDEO_ACK_ID)
}
