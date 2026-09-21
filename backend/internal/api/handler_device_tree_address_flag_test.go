package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
)

// m-1 (2026-09-22) — /device-configs/tree must publish, per model leaf, whether
// that model's controlled actions consume EdgeDevice.hardware_id as a PHYSICAL
// address.
//
// Why the field exists: the backend gate (validateEdgeDeviceAddress, via
// driverRequiresTargetAddress) only validates 1-254 for address-requiring
// drives and accepts ANY identifier for pure I2C/SPI models. The list-page
// wizard, unable to see that capability, judged by string SHAPE and replaced
// "UART1" with the default address 1 — writing an address a pure-I2C model
// never uses. This suite pins:
//
//	① the field is present on EVERY leaf (never omitted ⇒ false is decidable),
//	② its value is exactly the same judgement the write gate uses (one口径),
//	③ the pre-existing payload is untouched (additive, backward compatible).
//
// Every negative assertion carries a lower-bound/"classifier self-check"
// companion: a scan that found 0 leaves, or a tree where every flag was false,
// would pass vacuously and prove nothing (mutation-self-proof §四).

// treeLeafJSON is the leaf as the FRONTEND sees it. RequiresTargetAddress is a
// pointer on purpose: a missing key must be distinguishable from an explicit
// false, otherwise "the backend forgot to send the field" would silently look
// like "this model needs no address".
type treeLeafJSON struct {
	Type                  string   `json:"type"`
	Model                 string   `json:"model"`
	DisplayName           string   `json:"display_name"`
	HardwareTypes         []string `json:"hardware_types"`
	Description           string   `json:"description"`
	RequiresTargetAddress *bool    `json:"requires_target_address"`
}

type treeNodeJSON struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Children []treeNodeJSON `json:"children"`
	Drivers  []treeLeafJSON `json:"drivers"`
}

type envelopeJSON struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// fetchTreeRaw returns the raw data payload of GET /device-configs/tree so that
// callers can both decode it typed and inspect exact JSON key presence.
func fetchTreeRaw(t *testing.T, r *gin.Engine) []byte {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/device-configs/tree", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var env envelopeJSON
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("tree envelope is not valid JSON: %v: %s", err, w.Body.String())
	}
	if len(env.Data) == 0 {
		t.Fatalf("tree envelope carried no data: %s", w.Body.String())
	}
	return env.Data
}

// flattenTreeLeaves walks OEM → Category → Driver and returns every leaf in
// document order, using the same traversal the frontend's flattenDrivers uses.
func flattenTreeLeaves(t *testing.T, raw []byte) []treeLeafJSON {
	t.Helper()
	var nodes []treeNodeJSON
	if err := json.Unmarshal(raw, &nodes); err != nil {
		t.Fatalf("tree data is not the documented OEM → Category → Driver array: %v: %s", err, string(raw))
	}
	var out []treeLeafJSON
	var walk func([]treeNodeJSON)
	walk = func(list []treeNodeJSON) {
		for _, n := range list {
			out = append(out, n.Drivers...)
			walk(n.Children)
		}
	}
	walk(nodes)
	return out
}

func registryForTreeTest() *drivers.Registry {
	reg := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(reg)
	return reg
}

// ① Presence + ② one口径: every leaf carries the flag, and the flag equals the
// judgement of the SAME predicate the create/update gate calls.
func TestDeviceConfig_Tree_RequiresTargetAddressMatchesWriteGate(t *testing.T) {
	reg := registryForTreeTest()
	r, _ := setupDeviceTestWithRegistry(t, reg)

	raw := fetchTreeRaw(t, r)
	leaves := flattenTreeLeaves(t, raw)

	// Lower bound / classifier self-check: an empty scan makes every assertion
	// below vacuous.
	if len(leaves) < 5 {
		t.Fatalf("tree exposed only %d leaves; the scan is broken (registry has %d drivers)", len(leaves), len(reg.List()))
	}

	var addressed, addressless int
	byType := map[string]bool{}
	for _, leaf := range leaves {
		if leaf.Type == "" {
			t.Fatalf("leaf without a type in tree: %s", string(raw))
		}
		if leaf.RequiresTargetAddress == nil {
			t.Fatalf("leaf %q is missing requires_target_address; the wizard's compatibility branch would be forced on every model", leaf.Type)
		}
		want, cataloged := driverRequiresTargetAddress(reg, leaf.Type)
		wantFlag := cataloged && want
		if *leaf.RequiresTargetAddress != wantFlag {
			t.Fatalf("leaf %q requires_target_address=%v, but the write gate judges %v (cataloged=%v); tree and gate have drifted into two口径",
				leaf.Type, *leaf.RequiresTargetAddress, wantFlag, cataloged)
		}
		byType[leaf.Type] = *leaf.RequiresTargetAddress
		if *leaf.RequiresTargetAddress {
			addressed++
		} else {
			addressless++
		}
	}

	// Both classes must be represented, otherwise a "everything false" mutation
	// or an "everything true" mutation could not be told apart.
	if addressed == 0 {
		t.Fatalf("no leaf reported requires_target_address=true; the flag is dead and the wizard can never see an addressed model")
	}
	if addressless == 0 {
		t.Fatalf("no leaf reported requires_target_address=false; the flag is dead and the wizard can never skip the fabricated default address")
	}

	// The incident type (Modbus rain gauge) must be an addressed model …
	if v, ok := byType["sn3001_rain"]; !ok || !v {
		t.Fatalf("sn3001_rain must appear with requires_target_address=true, got present=%v value=%v", ok, v)
	}
	// … and pure I2C/SPI collectors must not demand one, otherwise the wizard
	// would keep writing an address they never use.
	for _, pureBusType := range []string{"bmp280", "lk_th01"} {
		v, ok := byType[pureBusType]
		if !ok {
			t.Fatalf("%s is a registered built-in driver but is absent from the tree", pureBusType)
		}
		if v {
			t.Fatalf("%s must not require a target address (pure I2C/SPI collector)", pureBusType)
		}
	}
}

// ③ Additive/backward compatibility: the payload a pre-m-1 consumer reads is
// byte-for-byte the same shape, and a consumer that does not know the new field
// still parses every value it used to read.
func TestDeviceConfig_Tree_NewFieldIsAdditive(t *testing.T) {
	reg := registryForTreeTest()
	r, _ := setupDeviceTestWithRegistry(t, reg)
	raw := fetchTreeRaw(t, r)

	leaves := flattenTreeLeaves(t, raw)
	if len(leaves) == 0 {
		t.Fatal("tree exposed no leaves; the compatibility check would be vacuous")
	}
	var foundLegacyLeaf bool
	for _, leaf := range leaves {
		if leaf.Type == "bmp280" {
			foundLegacyLeaf = true
			if leaf.DisplayName == "" || leaf.Model == "" {
				t.Fatalf("pre-existing leaf fields were emptied while adding the new one: %+v", leaf)
			}
			if len(leaf.HardwareTypes) == 0 {
				t.Fatalf("hardware_types disappeared from the leaf: %+v", leaf)
			}
		}
	}
	if !foundLegacyLeaf {
		t.Fatal("bmp280 leaf not found; cannot verify the legacy fields survived")
	}

	// A legacy consumer: it knows only the pre-m-1 leaf shape. json ignores the
	// unknown key, which is precisely the backward-compatibility guarantee.
	type legacyLeaf struct {
		Type          string   `json:"type"`
		Model         string   `json:"model"`
		DisplayName   string   `json:"display_name"`
		HardwareTypes []string `json:"hardware_types"`
		Description   string   `json:"description"`
	}
	type legacyNode struct {
		ID       string       `json:"id"`
		Name     string       `json:"name"`
		Children []legacyNode `json:"children"`
		Drivers  []legacyLeaf `json:"drivers"`
	}
	var legacy []legacyNode
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatalf("a pre-m-1 consumer can no longer parse the tree: %v", err)
	}
	legacyCount := 0
	var walk func([]legacyNode)
	walk = func(list []legacyNode) {
		for _, n := range list {
			legacyCount += len(n.Drivers)
			walk(n.Children)
		}
	}
	walk(legacy)
	if legacyCount != len(leaves) {
		t.Fatalf("legacy decode saw %d leaves, typed decode saw %d — the leaf shape changed in a non-additive way", legacyCount, len(leaves))
	}
}

// The DB-device-config branch of the tree synthesis must publish the same
// judgement as the registry branch: a model that is address-requiring as a
// built-in driver must not become address-less just because it also has a
// DeviceConfig row (or the reverse).
func TestDeviceConfig_Tree_DBLeafUsesSameAddressJudgement(t *testing.T) {
	reg := registryForTreeTest()
	r, db := setupDeviceTestWithRegistry(t, reg)

	if err := db.Create(&models.DeviceConfig{Name: "SN-3001 雨量计(模板)", DeviceType: "sn3001_rain", HardwareType: "uart", Status: "active"}).Error; err != nil {
		t.Fatalf("seeding DB device-config failed: %v", err)
	}
	if err := db.Create(&models.DeviceConfig{Name: "BMP280 模板", DeviceType: "bmp280", HardwareType: "i2c", Status: "active"}).Error; err != nil {
		t.Fatalf("seeding DB device-config failed: %v", err)
	}

	raw := fetchTreeRaw(t, r)
	leaves := flattenTreeLeaves(t, raw)

	seen := map[string]int{}
	for _, leaf := range leaves {
		if leaf.RequiresTargetAddress == nil {
			t.Fatalf("leaf %q (from the DB branch) is missing requires_target_address", leaf.Type)
		}
		want, cataloged := driverRequiresTargetAddress(reg, leaf.Type)
		if *leaf.RequiresTargetAddress != (cataloged && want) {
			t.Fatalf("DB-sourced leaf %q said requires_target_address=%v, write gate says %v", leaf.Type, *leaf.RequiresTargetAddress, cataloged && want)
		}
		seen[leaf.Type]++
	}
	// Floor: the synthetic device-configs above must actually have reached the
	// tree, otherwise this test asserts nothing about the DB branch.
	if seen["sn3001_rain"] == 0 || seen["bmp280"] == 0 {
		t.Fatalf("DB device-configs did not appear in the tree: %v", seen)
	}
}
