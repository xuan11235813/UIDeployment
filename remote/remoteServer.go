package main

import (
	"bytes"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type ServerEntity struct {
	Port      string
	IPAddress string
}

type ProjectEntity struct {
	ProjectNum  int
	ProjectName string
}

type LaneItem struct {
	LaneNum      int
	LaneMinCoord float32
	LaneMaxCoord float32
}

type LidarItem struct {
	LidarID   string
	Port      string
	IPAddress string
	LaneVec   []LaneItem
}

type Config struct {
	Server       ServerEntity
	Project      ProjectEntity
	LidarTypeVec []LidarItem
}

func main() {

	var configuration Config
	data, err := os.ReadFile("./config.json")
	if err != nil {
		fmt.Print(err)
	}

	err = json.Unmarshal(data, &configuration)
	if err != nil {
		fmt.Println("error:", err)
	}

	args := os.Args
	if len(args) == 1 {
		for i := 0; i < len(configuration.LidarTypeVec); i++ {
			go startNetLidar(configuration.LidarTypeVec[i], configuration.Server)
		}
		//startNetLidar()
	} else {
		fmt.Println(args[1])
		startFileSimulation(args[1], args[2], configuration.LidarTypeVec[0])
	}
	select {}
}

func startFileSimulation(fileName string, flag string, lidarProperty LidarItem) {
	var serverProperty ServerEntity
	file, err := os.Open(fileName)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer file.Close()
	byteWorker := StartByteWorker()
	switch flag {
	case "50":
		{
			fmt.Println("50Hz")
			StarEstimateWorker(byteWorker.chDataFrame, 720, lidarProperty, serverProperty, false)
		}
	case "100":
		{
			fmt.Println("100Hz")
			StarEstimateWorker(byteWorker.chDataFrame, 361, lidarProperty, serverProperty, false)
		}
	}

	for {
		tmp := make([]byte, 256)
		n, err := file.Read(tmp)
		if err != nil {
			if err != io.EOF {
				fmt.Println("read error:", err)
			}
			break
		}

		byteWorker.chBufferByte <- tmp[:n]
		time.Sleep(1 * time.Nanosecond)
	}
}

func startNetLidar(lidarProperty LidarItem, serverProperty ServerEntity) {
	conn, err := net.Dial("tcp", lidarProperty.IPAddress+":"+lidarProperty.Port)
	//conn, err := net.Dial("tcp", "192.168.80.7:6008")
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	defer conn.Close()
	byteWorker := StartByteWorker()
	StarEstimateWorker(byteWorker.chDataFrame, 361, lidarProperty, serverProperty, true)

	for {
		tmp := make([]byte, 256)
		n, err := conn.Read(tmp)
		if err != nil {
			if err != io.EOF {
				fmt.Println("read error:", err)
			}
			break
		}

		byteWorker.chBufferByte <- tmp[:n]
		//buf = append(buf, tmp[:n]...)
	}
}

// CSVFileToMap  reads csv file into slice of map
// slice is the line number
// map[string]string where key is column name
func CSVFileToMap(filePath string) (returnMap []map[string]string, err error) {

	// read csv file
	csvfile, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf(err.Error())
	}

	defer csvfile.Close()

	reader := csv.NewReader(csvfile)

	rawCSVdata, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf(err.Error())
	}

	header := []string{} // holds first row (header)
	for lineNum, record := range rawCSVdata {

		// for first row, build the header slice
		if lineNum == 0 {
			for i := 0; i < len(record); i++ {
				header = append(header, strings.TrimSpace(record[i]))
			}

		} else {
			// for each cell, map[string]string k=header v=value
			line := map[string]string{}
			for i := 0; i < len(record); i++ {
				line[header[i]] = record[i]
			}
			returnMap = append(returnMap, line)
		}
	}

	return
}

// MapToCSVFile  writes slice of map into csv file
// filterFields filters to only the fields in the slice, and maintains order when writing to file
func MapToCSVFile(inputSliceMap []map[string]string, filePath string, filterFields []string, flagInternal bool) (err error) {
	if flagInternal {
		var headers []string  // slice of each header field
		var line []string     // slice of each line field
		var csvLine string    // string of line converted to csv
		var CSVContent string // final output of csv containing header and lines

		// iter over slice to get all possible keys (csv header) in the maps
		// using empty Map[string]struct{} to get UNIQUE Keys; no value needed
		var headerMap = make(map[string]struct{})
		for _, record := range inputSliceMap {
			for k := range record {
				headerMap[k] = struct{}{}
			}
		}

		// convert unique headersMap to slice
		for headerValue := range headerMap {
			headers = append(headers, headerValue)
		}

		// filter to filteredFields and maintain order
		var filteredHeaders []string
		if len(filterFields) > 0 {
			for _, filterField := range filterFields {
				for _, headerValue := range headers {
					if filterField == headerValue {
						filteredHeaders = append(filteredHeaders, headerValue)
					}
				}
			}
		} else {
			filteredHeaders = append(filteredHeaders, headers...)
			sort.Strings(filteredHeaders) // alpha sort headers
		}

		// write headers as the first line
		csvLine, _ = WriteAsCSV(filteredHeaders)
		CSVContent += csvLine + "\n"

		// iter over inputSliceMap to get values for each map
		// maintain order provided in header slice
		// write to csv
		for _, record := range inputSliceMap {
			line = []string{}

			// lines
			for k := range filteredHeaders {
				line = append(line, record[filteredHeaders[k]])
			}
			csvLine, _ = WriteAsCSV(line)
			CSVContent += csvLine + "\n"
		}

		// make the dir incase it's not there
		err = os.MkdirAll(filepath.Dir(filePath), os.ModePerm)
		if err != nil {
			return err
		}

		// write out the csv contents to file
		ioutil.WriteFile(filePath, []byte(CSVContent), os.FileMode(0644))
		if err != nil {
			return err
		}
	}

	return
}

func WriteAsCSV(vals []string) (string, error) {
	b := &bytes.Buffer{}
	w := csv.NewWriter(b)
	err := w.Write(vals)
	if err != nil {
		return "", err
	}
	w.Flush()
	return strings.TrimSuffix(b.String(), "\n"), nil
}

type ByteWorker struct {
	chBufferByte chan []byte
	chFrame      chan []byte
	chDataFrame  chan []float64
}

func StartByteWorker() *ByteWorker {
	w := &ByteWorker{}
	w.chBufferByte = make(chan []byte, 6)
	w.chFrame = make(chan []byte, 6)
	w.chDataFrame = make(chan []float64, 6)
	go w.Implement()
	go w.ToNum()
	return w
}

func (w *ByteWorker) Implement() {
	var recBytes []byte
	for {
		inputBytes := <-w.chBufferByte

		for _, byteItem := range inputBytes {
			if len(recBytes) > 10 {
				recBytes = append(recBytes, byteItem)
				N := len(recBytes)
				if recBytes[N-10] == 0x02 && recBytes[N-9] == 0x05 && recBytes[N-7] == 0xfe && recBytes[N-5] == 0xfe && recBytes[N-3] == 0xfe && recBytes[N-1] == 0xfe {
					recHead := recBytes[N-10 : N]
					w.chFrame <- recBytes[:len(recBytes)-10]
					recBytes = recHead
				}
			} else {
				recBytes = append(recBytes, byteItem)
			}
		}
	}
}

func (w *ByteWorker) ToNum() {
	for testBytes := range w.chFrame {
		if len(testBytes) == 741 {
			N := len(testBytes)
			if testBytes[N-1] == 0x0a && testBytes[N-2] == 0x0d {
				var dataFrame []float64
				for i := 11; i < 732; i = i + 2 {
					dataFrame = append(dataFrame, float64(binary.BigEndian.Uint16(testBytes[i:i+2]))/1000.0)
				}
				w.chDataFrame <- dataFrame
			}
		}
		if len(testBytes) == 1460 {

			N := len(testBytes)
			if testBytes[N-1] == 0x0a && testBytes[N-2] == 0x0d {
				var dataFrame []float64
				for i := 11; i < 1450; i = i + 2 {
					dataFrame = append(dataFrame, float64(binary.BigEndian.Uint16(testBytes[i:i+2]))/1000.0)
				}
				w.chDataFrame <- dataFrame
			}
		}
	}
}

type Point2D struct {
	X          float64
	Y          float64
	R          float64
	index      int
	distToLine float64
}

type BackLine struct {
	//y = kx +b
	K float64
	b float64
}

type FrameCapture struct {
	Capture            []Point2D
	LeftPoint          Point2D
	RightPoint         Point2D
	Width              float64
	Height             float64
	Length             int32
	bestFitIndex       int
	bestFitLength      int
	currFrameIndex     int
	currFrameTimestamp int64
}

type VehicleCapture struct {
	Captures        []FrameCapture
	EstimatedHeight float64
	EstimatedWidth  float64
	isUpdated       bool
	ObjectNum       int
	bestLeft        int
	bestRight       int
	emptyFrame      int
}

type EstimateWorker struct {
	InputChannel    chan []float64
	ChBackground    chan []float64
	ChSignalBack    chan BackLine
	ChVehicles      chan VehicleCapture
	ChVehiclesJSON  chan VehicleNetJSON
	BackgroundAngle []float64
	LidarHeight     float64
	BackLength      int

	BackLineIsReady   bool
	BackgroundIsReady bool

	InputChannelBack   chan []float64
	InputChannelMain   chan []float64
	InputChannelDirect chan []float64

	OutputDirectData chan []Point2D
}

func StarEstimateWorker(input chan []float64, backLength int, lidarProperty LidarItem, serverProperty ServerEntity, isNetworkFlag bool) *EstimateWorker {
	w := &EstimateWorker{}
	w.InputChannel = input
	w.BackLength = backLength
	w.ChSignalBack = make(chan BackLine, 1)
	w.ChBackground = make(chan []float64, 3)
	w.ChVehicles = make(chan VehicleCapture, 3)
	w.ChVehiclesJSON = make(chan VehicleNetJSON, 3)
	w.InputChannelBack = make(chan []float64, 5)
	w.InputChannelMain = make(chan []float64, 5)
	w.InputChannelDirect = make(chan []float64, 5)

	w.OutputDirectData = make(chan []Point2D, 5)
	w.BackLineIsReady = false
	w.BackgroundIsReady = false

	for i := 0; i < w.BackLength; i++ {
		w.BackgroundAngle = append(w.BackgroundAngle, 0.0)
	}
	go w.DataDispatch(lidarProperty)
	go w.calculateTheBackground()
	go w.VehicleCuts(lidarProperty)
	go w.RawXYOutput(lidarProperty)
	go w.VehicleProcess(lidarProperty)
	go w.StartWebsocketDirect(lidarProperty)
	if isNetworkFlag {
		go w.NetworkSending(serverProperty)
	}
	return w
}

/***
 *						   ^	 Y axis
 *-------------------------|--------------------Lane
 *     |		   |       |
 *     |  vehicle  |       |
 *     |-----------|       |
 *  					   |
 *	grad -90(0)			   |						grad 90(180)
 *_________________________|______________________________> X axis
 */

func (w *EstimateWorker) calculateTheBackground() {
	fmt.Println("waiting")
	var maximalLidarDistance float64 = 15.0
	var flag int32 = 0
	var nullChangeTimes int32 = 0
	var waitingFrameNum = 0
	for dataFrame := range w.InputChannelBack {
		switch flag {
		case 0:
			{
				var changedNum int32 = 0
				for i := 0; i < len(w.BackgroundAngle); i++ {
					if dataFrame[i] >= 0 && dataFrame[i] <= maximalLidarDistance {
						if math.Abs(float64(dataFrame[i]-w.BackgroundAngle[i])) >= 0.05 {
							if dataFrame[i] > w.BackgroundAngle[i] {
								w.BackgroundAngle[i] = dataFrame[i]
								changedNum += 1
							}
						} else {
							if dataFrame[i] > w.BackgroundAngle[i] {
								var rate float64 = 0.5
								w.BackgroundAngle[i] = dataFrame[i]*rate + (1-rate)*w.BackgroundAngle[i]
							} else {
								var rate float64 = 0.2
								w.BackgroundAngle[i] = dataFrame[i]*rate + (1-rate)*w.BackgroundAngle[i]
							}
						}
					}
				}
				if changedNum == 0 {
					nullChangeTimes += 1
				}
				if nullChangeTimes >= 30 {
					/* BE CAUTIOUS */
					nullChangeTimes = 0
					flag = 1
					fmt.Println("finish back point generation")
				}
			}
		case 1:
			{

				/* calculate the back line */
				var angleInterval float64 = 0.5
				if w.BackLength == 720 {
					angleInterval = 0.25
				}
				var sum_x, sum_y, sum_xx, sum_xy, N float64 = 0.0, 0.0, 0.0, 0.0, 0.0

				for i := 0; i < len(w.BackgroundAngle); i++ {
					var p Point2D
					rad := (angleInterval*float64(i) - 90.0) / 180.0 * math.Pi
					p.Y = math.Cos(rad) * w.BackgroundAngle[i]
					p.X = math.Sin(rad) * w.BackgroundAngle[i]
					var centerIndex int = int(w.BackLength / 2)
					var centerLeft int = centerIndex - int(15.0/angleInterval)
					var centerRight int = centerIndex + int(15.0/angleInterval)
					if i >= centerLeft && i <= centerRight {
						sum_x += p.X
						sum_y += p.Y
						sum_xx += p.X * p.X
						sum_xy += p.X * p.Y
						N += 1.0
					}
				}

				K := (N*sum_xy - sum_x*sum_y) / (N*sum_xx - sum_x*sum_x)
				B := (sum_y / N) - (K * sum_x / N)
				fmt.Println("Y = KX+B K:=", K)
				fmt.Println("Y = KX+B B:=", B)
				var backline BackLine
				backline.b = B
				backline.K = K
				w.ChSignalBack <- backline
				back := make([]float64, len(w.BackgroundAngle))
				copy(back, w.BackgroundAngle[:])
				w.ChBackground <- back
				flag = 2
			}
		case 2:
			{
				var maximalWaitingNum = 100 * 60
				if waitingFrameNum <= maximalWaitingNum {
					waitingFrameNum++
				} else {
					waitingFrameNum = 0
					flag = 0
				}
			}
		}
	}
}

func polarToDescartes(polarData []float64, startAngle float64, intervalAngle float64) []Point2D {
	var points []Point2D
	for i := 0; i < len(polarData); i++ {
		var p Point2D
		rad := (startAngle + float64(i)*intervalAngle) / 180.0 * math.Pi
		p.Y = math.Cos(rad) * polarData[i]
		p.X = math.Sin(rad) * polarData[i]
		p.R = polarData[i]
		p.index = i
		points = append(points, p)
	}
	return points
}
func findDistance(line BackLine, point Point2D) float64 {
	ell := (line.K*point.X + line.b - point.Y) / (1 + line.K*line.K)
	return math.Abs(ell) * math.Sqrt(1+line.K*line.K)
}

func findDistance2p(p1 Point2D, p2 Point2D) float64 {
	return math.Sqrt((p1.X-p2.X)*(p1.X-p2.X) + (p1.Y-p2.Y)*(p1.Y-p2.Y))
}

/*
func checkIfConnected(f1 FrameCapture, f2 FrameCapture) int {
	f1L := f1.LeftPoint.index
	f1R := f1.RightPoint.index
	f2L := f2.LeftPoint.index
	f2R := f2.RightPoint.index
	var fMin int = 0.0
	var fMax int = 0.0
	if f1L <= f2L {
		fMin = f1L
	} else {
		fMin = f2L
	}
	if f1R >= f2R {
		fMax = f1R
	} else {
		fMax = f2R
	}
	var sumIndex int = 0
	for i := fMin; i <= fMax; i++ {
		if (f1L <= i && i <= f1R) && (f2L <= i && i <= f2R) {
			sumIndex += 1
		}
	}
	return sumIndex
}*/

func checkIfConnectedByBest(f1L int, f1R int, f2Left int, f2Right int) int {
	f2L := f2Left
	f2R := f2Right
	var fMin int = 0.0
	var fMax int = 0.0
	if f1L <= f2L {
		fMin = f1L
	} else {
		fMin = f2L
	}
	if f1R >= f2R {
		fMax = f1R
	} else {
		fMax = f2R
	}
	var sumIndex int = 0
	var totalEmpty int = 0
	for i := fMin; i <= fMax; i++ {
		if (f1L <= i && i <= f1R) && (f2L <= i && i <= f2R) {
			sumIndex += 1
		}
		if (f1L > i || i > f1R) && (f2L > i || i > f2R) {
			totalEmpty += 1
		}
	}
	if sumIndex == 0 {
		if totalEmpty <= 5 {
			sumIndex = 1
		}
	}
	return sumIndex
}

/*
func checkIfConnectedByHistory(f1 FrameCapture, f2Left int, f2Right int) int {
	f1L := f1.LeftPoint.index
	f1R := f1.RightPoint.index
	f2L := f2Left
	f2R := f2Right
	var fMin int = 0.0
	var fMax int = 0.0
	if f1L <= f2L {
		fMin = f1L
	} else {
		fMin = f2L
	}
	if f1R >= f2R {
		fMax = f1R
	} else {
		fMax = f2R
	}
	var sumIndex int = 0
	var totalEmpty int = 0
	for i := fMin; i <= fMax; i++ {
		if (f1L <= i && i <= f1R) && (f2L <= i && i <= f2R) {
			sumIndex += 1
		}
		if (f1L > i || i > f1R) && (f2L > i || i > f2R) {
			totalEmpty += 1
		}
	}
	if sumIndex == 0 {
		if totalEmpty <= 5 {
			sumIndex = 1
		}
	}
	return sumIndex
}
*/

func fullfillTheFrameCapture(points []Point2D, currFrameNum int) FrameCapture {
	var frame FrameCapture
	frame.Capture = points
	var leftIndex = 100000
	var rightIndex = 0
	for i := 0; i < len(points); i++ {
		if points[i].index < leftIndex {
			frame.LeftPoint = points[i]
			leftIndex = points[i].index
		}
		if points[i].index > rightIndex {
			frame.RightPoint = points[i]
			rightIndex = points[i].index
		}
	}
	//frame.LeftPoint = points[0]
	//frame.RightPoint = points[len(points)-1]
	//frame.Length = int32(points[len(points)-1].index - points[0].index)
	frame.Length = int32(math.Abs(float64(frame.LeftPoint.index) - float64(frame.RightPoint.index)))
	frame.Height = 0
	frame.Width = findDistance2p(frame.LeftPoint, frame.RightPoint)
	frame.bestFitIndex = -1
	frame.currFrameIndex = currFrameNum
	for i := 0; i < len(frame.Capture); i++ {
		if frame.Capture[i].distToLine > frame.Height {
			frame.Height = frame.Capture[i].distToLine
		}
	}
	return frame
}
func pointsCuts(points []Point2D, angleInterval float64, frameNum int) []FrameCapture {
	var pointsClips []FrameCapture
	var currentClip []Point2D
	var lastIndex int32 = -1
	for i := 0; i < len(points); i++ {
		//find a start
		if lastIndex == -1 {
			currentClip = append(currentClip, points[i])
			lastIndex = int32(points[i].index)
		} else {
			if math.Abs(float64(points[i].index-int(lastIndex))) <= 5.0/math.Abs(angleInterval) {
				lastIndex = int32(points[i].index)
				currentClip = append(currentClip, points[i])
			} else {
				frame := fullfillTheFrameCapture(currentClip, frameNum)
				pointsClips = append(pointsClips, frame)
				currentClip = nil
				currentClip = append(currentClip, points[i])
				lastIndex = int32(points[i].index)
			}
		}
	}
	if len(currentClip) > 1 {
		frame := fullfillTheFrameCapture(currentClip, frameNum)
		pointsClips = append(pointsClips, frame)
	}
	return pointsClips
}

func IsLegalStart(frame FrameCapture) bool {
	num := math.Abs(float64(frame.LeftPoint.index - frame.RightPoint.index))
	if num <= 10 {
		return false
	} else {
		return true
	}
}
func (w *EstimateWorker) DataDispatch(lidarProperty LidarItem) {
	for dataFrame := range w.InputChannel {
		select {
		case w.InputChannelBack <- dataFrame:
		default:
		}
		select {
		case w.InputChannelMain <- dataFrame:
		default:
		}
		select {
		case w.InputChannelDirect <- dataFrame:
		default:
		}
	}

}

type FrameMessage struct {
	Points []PointDTO `json:"points"`
}

type PointDTO struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	R float64 `json:"r"`
}

type Hub struct {
	clients    map[*websocket.Conn]bool
	register   chan *websocket.Conn
	unregister chan *websocket.Conn

	// only latest frame goes here
	broadcast chan []Point2D
}

func newHub() *Hub {
	return &Hub{
		clients:    make(map[*websocket.Conn]bool),
		register:   make(chan *websocket.Conn),
		unregister: make(chan *websocket.Conn),

		// IMPORTANT: size 1 = latest-frame only
		broadcast: make(chan []Point2D, 1),
	}
}

func (h *Hub) run() {
	for {
		select {

		case conn := <-h.register:
			h.clients[conn] = true

		case conn := <-h.unregister:
			if _, ok := h.clients[conn]; ok {
				delete(h.clients, conn)
				conn.Close()
			}

		case frame := <-h.broadcast:

			msg := FrameMessage{
				Points: make([]PointDTO, 0, len(frame)),
			}

			for _, p := range frame {
				msg.Points = append(msg.Points, PointDTO{
					X: p.X,
					Y: p.Y,
					R: p.R,
				})
			}

			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}

			// broadcast to all clients
			for conn := range h.clients {
				err := conn.WriteMessage(websocket.TextMessage, data)
				if err != nil {
					delete(h.clients, conn)
					conn.Close()
				}
			}
		}
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func (h *Hub) wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	h.register <- conn

	go func() {
		defer func() {
			h.unregister <- conn
		}()

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}()
}

func (h *Hub) startFrameConsumer(in <-chan []Point2D) {
	for frame := range in {

		// drain old frames, keep only newest
	drain:
		for {
			select {
			case next := <-in:
				frame = next
			default:
				break drain
			}
		}

		// non-blocking send to hub (avoid blocking producer chain)
		select {
		case h.broadcast <- frame:
		default:
			// drop old queued frame, keep latest
			<-h.broadcast
			h.broadcast <- frame
		}
	}
}

func (w *EstimateWorker) RawXYOutput(lidarProperty LidarItem) {
	var angleInterval float64 = 0.5
	if w.BackLength == 720 {
		angleInterval = 0.25
	}
	for dataFrame := range w.InputChannelDirect {
		points := polarToDescartes(dataFrame, -90.0, angleInterval)
		w.OutputDirectData <- points
	}

}

func (w *EstimateWorker) StartWebsocketDirect(lidarProperty LidarItem) {
	hub := newHub()
	go hub.run()
	go hub.startFrameConsumer(w.OutputDirectData)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", hub.wsHandler)
	portNum, _ := strconv.Atoi(lidarProperty.Port)
	idNum, _ := strconv.Atoi(lidarProperty.LidarID)
	portNum = portNum + idNum

	server := &http.Server{
		Addr:    ":" + strconv.Itoa(portNum),
		Handler: mux,
	}
	log.Fatal(server.ListenAndServe())
}

func (w *EstimateWorker) VehicleCuts(lidarProperty LidarItem) {
	var currentBackLine BackLine
	var lidarHeight = 0.0
	var leftXLimit = -4.0
	var rightXLimit = 4.0
	var leftAngleLimit = 40
	var rightAngleLimit = 320
	var maximalTrackCaptureNum = 200
	var minimalDistanceThreshold = 1.0
	var liveVehicles []VehicleCapture
	var currBackground []float64
	var currTimestamp int64
	for i := 0; i < w.BackLength; i++ {
		currBackground = append(currBackground, 0.0)
	}
	var objectNum int = 0
	var frameNumAccu = 0
	for {
		select {
		case dataFrame := <-w.InputChannelMain:
			{
				currTimestamp = time.Now().UnixMilli()
				frameNumAccu = frameNumAccu + 1
				if w.BackLineIsReady && w.BackgroundIsReady {
					var angleInterval float64 = 0.5
					if w.BackLength == 720 {
						angleInterval = 0.25
					}

					points := polarToDescartes(dataFrame, -90.0, angleInterval)
					var pointsCleared []Point2D
					for i := 0; i < len(points); i++ {
						if points[i].X >= leftXLimit && points[i].X <= rightXLimit && i >= leftAngleLimit && i <= rightAngleLimit {
							if math.Abs(points[i].R-currBackground[i]) >= 0.1 {
								lineDistance := findDistance(currentBackLine, points[i])
								if lineDistance < lidarHeight {
									if lineDistance >= minimalDistanceThreshold {
										points[i].distToLine = lineDistance
										pointsCleared = append(pointsCleared, points[i])
									}
								}
							}
						}
					}
					pointsClips := pointsCuts(pointsCleared, angleInterval, frameNumAccu)
					if len(liveVehicles) == 0 {
						for _, frameCaptureItem := range pointsClips {
							frameCaptureItem.currFrameTimestamp = currTimestamp
							if IsLegalStart(frameCaptureItem) {
								var vehicleCaptureItem VehicleCapture
								vehicleCaptureItem.isUpdated = false
								vehicleCaptureItem.Captures = append(vehicleCaptureItem.Captures, frameCaptureItem)
								vehicleCaptureItem.bestLeft = frameCaptureItem.LeftPoint.index
								vehicleCaptureItem.bestRight = frameCaptureItem.RightPoint.index
								liveVehicles = append(liveVehicles, vehicleCaptureItem)
							}
						}
					} else {
						for i := 0; i < len(liveVehicles); i++ {
							liveVehicles[i].isUpdated = false
						}
						/* calculate the alignment */
						for i := 0; i < len(liveVehicles); i++ {
							for j := 0; j < len(pointsClips); j++ {
								connectLength := checkIfConnectedByBest(liveVehicles[i].bestLeft, liveVehicles[i].bestRight, pointsClips[j].LeftPoint.index, pointsClips[j].RightPoint.index)
								if connectLength > 0 {
									if connectLength > pointsClips[j].bestFitLength {
										pointsClips[j].bestFitIndex = i
										pointsClips[j].bestFitLength = connectLength
									}
								}

							}
						}
						/* add the legal points */
						for i := 0; i < len(pointsClips); i++ {
							pointsClips[i].currFrameTimestamp = currTimestamp
						}
						for i := 0; i < len(pointsClips); i++ {
							pointsClips[i].currFrameTimestamp = currTimestamp
							if pointsClips[i].bestFitIndex >= 0 {
								if !liveVehicles[pointsClips[i].bestFitIndex].isUpdated {
									liveVehicles[pointsClips[i].bestFitIndex].emptyFrame = 0
									liveVehicles[pointsClips[i].bestFitIndex].isUpdated = true
									liveVehicles[pointsClips[i].bestFitIndex].Captures = append(liveVehicles[pointsClips[i].bestFitIndex].Captures, pointsClips[i])
									if liveVehicles[pointsClips[i].bestFitIndex].bestLeft > pointsClips[i].LeftPoint.index {
										liveVehicles[pointsClips[i].bestFitIndex].bestLeft = pointsClips[i].LeftPoint.index
									}
									if liveVehicles[pointsClips[i].bestFitIndex].bestRight < pointsClips[i].RightPoint.index {
										liveVehicles[pointsClips[i].bestFitIndex].bestRight = pointsClips[i].RightPoint.index
									}
								} else {
									liveVehicles[pointsClips[i].bestFitIndex].emptyFrame = 0
									N := len(liveVehicles[pointsClips[i].bestFitIndex].Captures)
									liveVehicles[pointsClips[i].bestFitIndex].Captures[N-1].Capture = append(liveVehicles[pointsClips[i].bestFitIndex].Captures[N-1].Capture, pointsClips[i].Capture...)
									currFrameData := fullfillTheFrameCapture(liveVehicles[pointsClips[i].bestFitIndex].Captures[N-1].Capture, frameNumAccu)
									currFrameData.currFrameTimestamp = currTimestamp
									liveVehicles[pointsClips[i].bestFitIndex].Captures[N-1] = currFrameData
									/*
										if liveVehicles[pointsClips[i].bestFitIndex].bestLeft > currFrameData.LeftPoint.index {
											liveVehicles[pointsClips[i].bestFitIndex].bestLeft = currFrameData.LeftPoint.index
										}
										if liveVehicles[pointsClips[i].bestFitIndex].bestRight < currFrameData.RightPoint.index {
											liveVehicles[pointsClips[i].bestFitIndex].bestRight = currFrameData.RightPoint.index
										}*/
									liveVehicles[pointsClips[i].bestFitIndex].bestLeft = currFrameData.LeftPoint.index
									liveVehicles[pointsClips[i].bestFitIndex].bestRight = currFrameData.RightPoint.index
								}
							} else {
								var vehicleCaptureItem VehicleCapture
								if IsLegalStart(pointsClips[i]) {
									vehicleCaptureItem.isUpdated = true
									vehicleCaptureItem.emptyFrame = 0
									vehicleCaptureItem.Captures = append(vehicleCaptureItem.Captures, pointsClips[i])
									vehicleCaptureItem.bestLeft = pointsClips[i].LeftPoint.index
									vehicleCaptureItem.bestRight = pointsClips[i].RightPoint.index
									liveVehicles = append(liveVehicles, vehicleCaptureItem)
								}
							}
						}

						/* eliminate the finished vehicles */
						var vehiclesNew []VehicleCapture
						for _, item := range liveVehicles {
							if !item.isUpdated {
								item.emptyFrame = item.emptyFrame + 1
							}
							if !item.isUpdated && item.emptyFrame >= 5 {
								item.ObjectNum = objectNum
								w.ChVehicles <- item
								objectNum += 1
							} else {
								if len(item.Captures) > maximalTrackCaptureNum {
									item.Captures = item.Captures[1:]
								}
								vehiclesNew = append(vehiclesNew, item)
							}
						}
						liveVehicles = vehiclesNew
					}

				} else {
					_ = dataFrame
				}
			}
		case signal := <-w.ChSignalBack:
			{
				currentBackLine = signal
				var point Point2D
				point.X = 0
				point.Y = 0
				lidarHeight = findDistance(currentBackLine, point)
				w.LidarHeight = lidarHeight
				w.BackLineIsReady = true
			}
		case back := <-w.ChBackground:
			{
				copy(currBackground, back)
				w.BackgroundIsReady = true
			}
		}
	}

}

type VehicleNet struct {
	pointCloud      [][]float64
	startTimestamp  int64
	endTimestamp    int64
	evaluatedHeight float64
	evaluatedWidth  float64
	centerX         float64
	laneNum         int32
}

// VehicleNetJSON is a JSON-serializable version of VehicleNet without pointCloud.
type VehicleNetJSON struct {
	StartTimestamp  int64   `json:"startTimestamp"`
	EndTimestamp    int64   `json:"endTimestamp"`
	EvaluatedHeight float64 `json:"evaluatedHeight"`
	EvaluatedWidth  float64 `json:"evaluatedWidth"`
	CenterX         float64 `json:"centerX"`
	LaneNum         int32   `json:"laneNum"`
	LidarID         string  `json:"lidarID"`
}

func (w *EstimateWorker) LaneDetermine(vehicle *VehicleNet, lidarProperty LidarItem) {
	vehicle.laneNum = -1
	var minCloseCenterDistance = 1000
	var minCarWidth = 1.4
	var closeLaneNum = -1
	for i := 0; i < len(lidarProperty.LaneVec); i++ {
		if vehicle.centerX > float64(lidarProperty.LaneVec[i].LaneMinCoord) && vehicle.centerX < float64(lidarProperty.LaneVec[i].LaneMaxCoord) {
			var currentCloseDistance = math.Abs(vehicle.centerX - float64(lidarProperty.LaneVec[i].LaneMaxCoord)*0.5 - float64(lidarProperty.LaneVec[i].LaneMinCoord)*0.5)
			if currentCloseDistance < float64(minCloseCenterDistance) {
				closeLaneNum = lidarProperty.LaneVec[i].LaneNum
			}
		}
	}
	vehicle.laneNum = int32(closeLaneNum)
	if vehicle.evaluatedWidth <= minCarWidth {
		vehicle.laneNum = -1
	}
}

func (w *EstimateWorker) EvaluateTheVehicle(frameData []FrameCapture) VehicleNet {
	var vehicle VehicleNet
	N := len(frameData)
	var currZ float64 = 0.0
	var heightVec []float64
	var widthVec []float64
	var vehicleMinX float64 = 100000
	var vehicleMaxX float64 = -100000
	vehicle.startTimestamp = frameData[0].currFrameTimestamp
	vehicle.endTimestamp = frameData[N-1].currFrameTimestamp
	for _, item := range frameData {
		currZ += 0.3
		var minX float64 = 10000
		var maxX float64 = -10000
		for _, capturePoint := range item.Capture {
			var point []float64
			point = append(point, capturePoint.X)
			point = append(point, currZ)
			point = append(point, w.LidarHeight-capturePoint.Y)
			vehicle.pointCloud = append(vehicle.pointCloud, point)
			if point[0] <= minX {
				minX = point[0]
			}
			if point[0] >= maxX {
				maxX = point[0]
			}
			heightVec = append(heightVec, point[2])
		}
		if minX < vehicleMinX {
			vehicleMinX = minX
		}
		if maxX > vehicleMaxX {
			vehicleMaxX = maxX
		}
		widthVec = append(widthVec, math.Abs(maxX-minX))

	}
	sort.Float64s(widthVec)
	sort.Float64s(heightVec)
	vehicle.evaluatedWidth = widthVec[len(widthVec)-1]
	vehicle.evaluatedHeight = heightVec[int(float64(len(heightVec)-1)*0.995)]
	vehicle.centerX = (vehicleMaxX + vehicleMinX) / 2.0
	return vehicle
}

func (w *EstimateWorker) SaveToFile(vehicleItem VehicleCapture) {
	var currZ float64 = 0.0
	var stringTable []map[string]string
	for i := 0; i < len(vehicleItem.Captures); i++ {
		currZ += 0.2
		for j := 0; j < len(vehicleItem.Captures[i].Capture); j++ {
			currX := vehicleItem.Captures[i].Capture[j].X
			currY := vehicleItem.Captures[i].Capture[j].Y

			currM := map[string]string{}
			currM["x"] = strconv.FormatFloat(currX, 'f', -1, 64)
			currM["y"] = strconv.FormatFloat(currZ, 'f', -1, 64)
			currM["z"] = strconv.FormatFloat(w.LidarHeight-currY, 'f', -1, 64)
			stringTable = append(stringTable, currM)
		}
	}
	startIndex := strconv.Itoa(vehicleItem.Captures[0].currFrameIndex)
	endIndex := strconv.Itoa(vehicleItem.Captures[len(vehicleItem.Captures)-1].currFrameIndex)
	var header = []string{"x", "y", "z"}
	var fileName string = "./objFile/obj" + strconv.Itoa(vehicleItem.ObjectNum) + "-" + startIndex + "-" + endIndex + "-" + ".csv"
	MapToCSVFile(stringTable, fileName, header, false)
}

func (w *EstimateWorker) VehicleProcess(lidarProperty LidarItem) {

	for vehicleItem := range w.ChVehicles {
		startIndex := strconv.Itoa(vehicleItem.Captures[0].currFrameIndex)
		endIndex := strconv.Itoa(vehicleItem.Captures[len(vehicleItem.Captures)-1].currFrameIndex)
		vehicleNet := w.EvaluateTheVehicle(vehicleItem.Captures)
		w.LaneDetermine(&vehicleNet, lidarProperty)
		if vehicleNet.laneNum > 0 {
			w.SaveToFile(vehicleItem)

			fmt.Println("==============-" + startIndex + "-" + endIndex + "-=============")
			fmt.Println("center X := ", vehicleNet.centerX, "with lane index", vehicleNet.laneNum)
			fmt.Println("width: =", vehicleNet.evaluatedWidth)
			fmt.Println("height: =", vehicleNet.evaluatedHeight)

			// Create JSON-serializable struct and send to channel
			vehicleJSON := VehicleNetJSON{
				StartTimestamp:  vehicleNet.startTimestamp,
				EndTimestamp:    vehicleNet.endTimestamp,
				EvaluatedHeight: vehicleNet.evaluatedHeight,
				EvaluatedWidth:  vehicleNet.evaluatedWidth,
				CenterX:         vehicleNet.centerX,
				LaneNum:         vehicleNet.laneNum,
				LidarID:         lidarProperty.LidarID,
			}

			// Save to date-based file (YYYY_MM_DD.csv)
			currentDate := time.Now().Format("2006_01_02")
			filename := currentDate + ".csv"
			file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				fmt.Printf("Failed to open %s: %v\n", filename, err)
			} else {
				line := fmt.Sprintf("%d,%d,%.3f,%.3f,%.3f,%d,%s\n",
					vehicleJSON.StartTimestamp,
					vehicleJSON.EndTimestamp,
					vehicleJSON.EvaluatedHeight,
					vehicleJSON.EvaluatedWidth,
					vehicleJSON.CenterX,
					vehicleJSON.LaneNum,
					vehicleJSON.LidarID)
				if _, err := file.WriteString(line); err != nil {
					fmt.Printf("Failed to write to %s: %v\n", filename, err)
				}
				file.Close()
			}

			select {
			case w.ChVehiclesJSON <- vehicleJSON:
			default:
				// channel full, drop vehicle JSON to avoid blocking
				fmt.Printf("Channel full, dropping vehicle JSON (lane %d)\n", vehicleJSON.LaneNum)
			}
		}
	}
}

func (w *EstimateWorker) NetworkSending(serverProperty ServerEntity) {
	url := "http://" + serverProperty.IPAddress + ":" + serverProperty.Port + "/vehicles"
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:       10,
			IdleConnTimeout:    90 * time.Second,
			DisableCompression: false,
			DisableKeepAlives:  false,
		},
	}

	for vehicle := range w.ChVehiclesJSON {
		// Marshal vehicle to JSON
		jsonData, err := json.Marshal(vehicle)
		if err != nil {
			fmt.Printf("Failed to marshal vehicle JSON: %v\n", err)
			continue
		}

		// Retry loop with connection reestablishment
		for {
			req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
			if err != nil {
				fmt.Printf("Failed to create request: %v\n", err)
				break // cannot recover
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				fmt.Printf("POST request failed: %v. Reconnecting in 5 seconds...\n", err)
				time.Sleep(5 * time.Second)
				// Optionally recreate client if needed
				continue
			}

			// Read and print response
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			fmt.Printf("Server response: %s\n", string(body))
			break // success, move to next vehicle
		}
	}
}
