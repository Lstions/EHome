package deviceinit

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&models.CalibrationCache{}); err != nil {
		t.Fatal(err)
	}
	return db
}
func TestGetInitSequenceBMP280IsRuntimeConsumable(t *testing.T) {
	o := NewOrchestrator(nil, nil, nil)
	s := o.GetInitSequence("bmp280")
	if len(s) != 5 || s[0].Name != "reset" || s[0].Data[0] != 0xE0 || s[2].Name != "read_calib" || s[2].ReadSize != 24 {
		t.Fatalf("unexpected BMP280 sequence: %#v", s)
	}
}
func TestInitRequestIDsAreGloballyUniqueAndNeverZero(t *testing.T) {
	atomic.StoreUint32(&nextInitRequestID, 0)
	seen := map[uint32]bool{}
	for range 1000 {
		id := allocateInitRequestID()
		if id == 0 || seen[id] {
			t.Fatalf("invalid or duplicate request id %d", id)
		}
		seen[id] = true
	}
}
func TestCalibrationIsUpsertedPerEdgeDevice(t *testing.T) {
	db := testDB(t)
	o := NewOrchestrator(db, nil, nil)
	data := make([]byte, 24)
	data[0] = 1
	a := models.EdgeDevice{ID: 1, NodeID: "node", Type: "bmp280"}
	b := models.EdgeDevice{ID: 2, NodeID: "node", Type: "bmp280"}
	o.saveCalibData(a, data)
	data[0] = 2
	o.saveCalibData(b, data)
	data[0] = 3
	o.saveCalibData(a, data)
	var rows []models.CalibrationCache
	if err := db.Order("edge_device_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].EdgeDeviceID != 1 || rows[1].EdgeDeviceID != 2 || rows[0].Data[:2] != "03" {
		t.Fatalf("calibration must be isolated and upserted: %#v", rows)
	}
}
func TestInvalidCalibrationIsNotPersisted(t *testing.T) {
	db := testDB(t)
	o := NewOrchestrator(db, nil, nil)
	o.saveCalibData(models.EdgeDevice{ID: 1, NodeID: "n", Type: "bmp280"}, make([]byte, 23))
	var n int64
	db.Model(&models.CalibrationCache{}).Count(&n)
	if n != 0 {
		t.Fatalf("short calibration persisted: %d", n)
	}
}

func TestCalibrationLengthContentAndDBFailuresAreErrors(t *testing.T) {
	db := testDB(t)
	o := NewOrchestrator(db, nil, nil)
	device := models.EdgeDevice{ID: 1, NodeID: "n", Type: "bmp280"}
	if err := o.saveCalibData(device, make([]byte, 23)); err == nil {
		t.Fatal("23-byte calibration must fail")
	}
	if err := o.saveCalibData(device, make([]byte, 25)); err == nil {
		t.Fatal("25-byte calibration must fail")
	}
	if err := o.saveCalibData(device, make([]byte, 24)); err == nil {
		t.Fatal("all-zero calibration must fail content validation")
	}
	allFF := make([]byte, 24)
	for i := range allFF {
		allFF[i] = 0xff
	}
	if err := o.saveCalibData(device, allFF); err == nil {
		t.Fatal("all-ff calibration must fail content validation")
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 24)
	data[0] = 1
	if err := o.saveCalibData(device, data); err == nil {
		t.Fatal("closed DB must fail calibration persistence")
	}
}

type failingPublisher struct{ calls atomic.Int32 }

func (p *failingPublisher) Publish(string, []byte) error {
	p.calls.Add(1)
	return fmt.Errorf("publish failed")
}

type blockingPublisher struct {
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (p *blockingPublisher) Publish(string, []byte) error {
	select {
	case <-p.started:
	default:
		close(p.started)
	}
	<-p.release
	close(p.finished)
	return fmt.Errorf("blocked publish failed")
}

func TestInitFailsFastAndCanBeRetried(t *testing.T) {
	p := &failingPublisher{}
	o := NewOrchestrator(nil, p, nil)
	device := models.EdgeDevice{ID: 10, NodeID: "node", Type: "bmp280"}
	if err := o.InitEdgeDevice(device, "node"); err == nil {
		t.Fatal("first step failure must fail init")
	}
	if got := p.calls.Load(); got != 1 {
		t.Fatalf("fail-fast published %d steps, want 1", got)
	}
	if o.HasActiveInit(device.ID) || o.IsInitialized(device.ID) {
		t.Fatal("failed init must not remain active/completed")
	}
	if err := o.InitEdgeDevice(device, "node"); err == nil {
		t.Fatal("retry should execute and fail independently")
	}
}

func TestConcurrentInitIfNeededReservesExactlyOnce(t *testing.T) {
	p := &blockingPublisher{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	o := NewOrchestrator(nil, p, nil)
	device := models.EdgeDevice{ID: 11, NodeID: "node", Type: "unknown"}
	// Unknown types fail closed before reservation.
	if o.InitIfNeeded(device, "node") {
		t.Fatal("unknown type must not reserve")
	}
	device.Type = "bmp280"
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- o.InitIfNeeded(device, "node") }()
	}
	select {
	case <-p.started:
	case <-time.After(time.Second):
		t.Fatal("first initialization did not reach publisher")
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	started := 0
	for i := 0; i < 2; i++ {
		select {
		case accepted := <-results:
			if accepted {
				started++
			}
		case <-timer.C:
			t.Fatal("reservation did not complete")
		}
	}
	if started != 1 {
		t.Fatalf("reserved %d concurrent initializations before release, want 1", started)
	}
	close(p.release)
	select {
	case <-p.finished:
	case <-time.After(time.Second):
		t.Fatal("first initialization did not finish after release")
	}
}
func TestInitStateIsEdgeDeviceScoped(t *testing.T) {
	o := NewOrchestrator(nil, nil, nil)
	o.cache[1] = &InitState{Completed: true}
	o.cache[2] = &InitState{Completed: false, InProgress: true}
	if !o.IsInitialized(1) || o.IsInitialized(2) || !o.HasActiveInit(2) {
		t.Fatal("init state leaked across edge devices")
	}
	o.ClearCache(1)
	if o.IsInitialized(1) {
		t.Fatal("cache was not cleared")
	}
}
func TestInitIfNeededRejectsUnknownType(t *testing.T) {
	if NewOrchestrator(nil, nil, nil).InitIfNeeded(models.EdgeDevice{ID: 1, Type: "unknown"}, "node") {
		t.Fatal("unknown driver init started")
	}
}

type capturePublisher struct {
	payload   []byte
	published chan struct{}
}

func (p *capturePublisher) Publish(_ string, payload []byte) error {
	p.payload = append([]byte(nil), payload...)
	close(p.published)
	return nil
}

func TestWriteCmdFrameCarriesEdgeDeviceID(t *testing.T) {
	p := &capturePublisher{published: make(chan struct{})}
	o := NewOrchestrator(nil, p, nil)
	done := make(chan error, 1)
	go func() { _, err := o.sendAndWait("node", 42, "read", 7, 99, []byte{0xD0}, 1, time.Second); done <- err }()
	<-p.published
	dec, err := frame.NewDecoder(p.payload)
	if err != nil {
		t.Fatal(err)
	}
	var edgeDeviceID uint64
	for {
		field, err := dec.NextField()
		if err == frame.ErrEndOfFrame {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if field.FieldNum == 5 {
			edgeDeviceID = frame.GetUint64(field)
		}
	}
	if edgeDeviceID != 42 {
		t.Fatalf("WriteCmd edge_device_id = %d, want 42", edgeDeviceID)
	}
	o.HandleDataReportAck("node", 42, 99, 0, []byte{0})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDataReportAckRequiresExactPendingEdgeDevice(t *testing.T) {
	o := NewOrchestrator(nil, nil, nil)
	first, second := make(chan pendingResult, 1), make(chan pendingResult, 1)
	o.pendingResp[700] = pendingResponse{nodeID: "node", edgeDeviceID: 9, stepName: "read_calib", responseKind: responseData, expectedReadSize: 1, response: first}
	o.pendingResp[701] = pendingResponse{nodeID: "node", edgeDeviceID: 10, stepName: "read_calib", responseKind: responseData, expectedReadSize: 1, response: second}
	rawA, rawB := []byte{0x7A}, []byte{0x7B}
	o.HandleDataReportAck("node", 10, 700, 0, rawB)
	o.HandleDataReportAck("node", 9, 999, 0, rawA)
	select {
	case <-first:
		t.Fatal("wrong/unknown correlation completed first")
	default:
	}
	o.HandleDataReportAck("node", 10, 701, 0, rawB)
	o.HandleDataReportAck("node", 9, 700, 0, rawA)
	if got := (<-first).raw; string(got) != string(rawA) {
		t.Fatalf("first response=%x want=%x", got, rawA)
	}
	if got := (<-second).raw; string(got) != string(rawB) {
		t.Fatalf("second response=%x want=%x", got, rawB)
	}
}

func TestWriteResponseCompletesOnlyWriteOnlyPendingStep(t *testing.T) {
	o := NewOrchestrator(nil, nil, nil)
	writeCh, readCh := make(chan pendingResult, 1), make(chan pendingResult, 1)
	o.pendingResp[1] = pendingResponse{nodeID: "node", edgeDeviceID: 1, stepName: "reset", responseKind: responseWrite, response: writeCh}
	o.pendingResp[2] = pendingResponse{nodeID: "node", edgeDeviceID: 1, stepName: "read_chip_id", responseKind: responseData, expectedReadSize: 1, response: readCh}
	o.HandleWriteResponse("node", 1, true, 0, "")
	o.HandleWriteResponse("node", 2, true, 0, "")
	if got := <-writeCh; got.err != nil {
		t.Fatalf("write response error=%v", got.err)
	}
	select {
	case <-readCh:
		t.Fatal("WriteRsp completed read command")
	default:
	}
}

func TestDataReportErrorsAndLengthFailFast(t *testing.T) {
	o := NewOrchestrator(nil, nil, nil)
	result := make(chan pendingResult, 1)
	o.pendingResp[3] = pendingResponse{nodeID: "node-a", edgeDeviceID: 7, responseKind: responseData, expectedReadSize: 2, response: result}
	o.HandleDataReportAck("node-b", 7, 3, 0, []byte{1, 2})
	select {
	case <-result:
		t.Fatal("wrong node consumed pending response")
	default:
	}
	o.HandleDataReportAck("node-a", 7, 3, 0, []byte{1})
	if got := <-result; got.err == nil {
		t.Fatal("short read must return typed error")
	}

	o.pendingResp[4] = pendingResponse{nodeID: "node-a", edgeDeviceID: 7, responseKind: responseData, expectedReadSize: 2, response: result}
	o.HandleDataReportAck("node-a", 7, 4, 0x01, []byte{1, 2})
	if got := <-result; got.err == nil {
		t.Fatal("device RX error must return typed error")
	}
}

func TestWriteFailureRoutesToReadAndWritePending(t *testing.T) {
	o := NewOrchestrator(nil, nil, nil)
	write, read := make(chan pendingResult, 1), make(chan pendingResult, 1)
	o.pendingResp[5] = pendingResponse{nodeID: "node", responseKind: responseWrite, response: write}
	o.pendingResp[6] = pendingResponse{nodeID: "node", responseKind: responseData, expectedReadSize: 1, response: read}
	o.HandleWriteResponse("node", 5, false, 9, "write failed")
	o.HandleWriteResponse("node", 6, false, 9, "read failed")
	if (<-write).err == nil || (<-read).err == nil {
		t.Fatal("write failures must fail both pending kinds")
	}
}
