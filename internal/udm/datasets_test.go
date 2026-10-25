package udm

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/svinson1121/vectorcore-hss/internal/models"
)

const testSUPI = "imsi-001010000000001"

func newDataSetsRouter(t *testing.T) *chi.Mux {
	t.Helper()
	s, db := newTestUDMServer(t)
	// A subscriber with no APNs: smfSelData must still be present.
	mustCreateUDM(t, db, &models.Subscriber{IMSI: "001010000000002", AUCID: 2})
	r := chi.NewRouter()
	s.mountRoutes(r)
	return r
}

func doGet(t *testing.T, r http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// compactJSON normalises a JSON document so two bodies can be compared.
func compactJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, bytes.TrimSpace(raw)); err != nil {
		t.Fatalf("compact %s: %v", raw, err)
	}
	return buf.String()
}

func getDataSets(t *testing.T, r http.Handler, path string) map[string]json.RawMessage {
	t.Helper()
	rec := doGet(t, r, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d body=%s", path, rec.Code, rec.Body.String())
	}
	var sets map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &sets); err != nil {
		t.Fatalf("GET %s: decode: %v body=%s", path, err, rec.Body.String())
	}
	return sets
}

// requireSameAsResource asserts a GetDataSets entry is identical to the
// per-resource endpoint's body, so old and new AMFs see the same data.
func requireSameAsResource(t *testing.T, r http.Handler, sets map[string]json.RawMessage, field, resourcePath string) {
	t.Helper()
	got, ok := sets[field]
	if !ok {
		t.Fatalf("%s missing from GetDataSets response: %v", field, keys(sets))
	}
	rec := doGet(t, r, resourcePath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d body=%s", resourcePath, rec.Code, rec.Body.String())
	}
	if a, b := compactJSON(t, got), compactJSON(t, rec.Body.Bytes()); a != b {
		t.Fatalf("%s differs from %s:\n datasets: %s\n resource: %s", field, resourcePath, a, b)
	}
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The exact request current Open5GS AMFs send during registration
// (src/amf/nudm-build.c amf_nudm_sdm_build_get_datasets), checked against
// the per-resource calls older Open5GS AMFs make.
func TestGetDataSetsOpen5GSAMFRequest(t *testing.T) {
	r := newDataSetsRouter(t)
	plmn := url.QueryEscape(`{"mcc":"001","mnc":"01"}`)

	sets := getDataSets(t, r, "/nudm-sdm/v2/"+testSUPI+"?dataset-names=AM,SMF_SEL&plmn-id="+plmn)
	if len(sets) != 2 {
		t.Fatalf("expected only amData and smfSelData, got %v", keys(sets))
	}
	requireSameAsResource(t, r, sets, "amData", "/nudm-sdm/v2/"+testSUPI+"/am-data")
	requireSameAsResource(t, r, sets, "smfSelData", "/nudm-sdm/v2/"+testSUPI+"/smf-select-data")
}

func TestGetDataSetsAllSupportedSkipsUnknown(t *testing.T) {
	r := newDataSetsRouter(t)

	for _, ver := range []string{"v1", "v2"} {
		base := "/nudm-sdm/" + ver + "/" + testSUPI
		sets := getDataSets(t, r, base+"?dataset-names=AM,SM,UEC_SMF,SMF_SEL,TRACE,SOME_FUTURE_SET")
		if len(sets) != 4 {
			t.Fatalf("%s: expected 4 data sets (unsupported skipped), got %v", ver, keys(sets))
		}
		requireSameAsResource(t, r, sets, "amData", base+"/am-data")
		requireSameAsResource(t, r, sets, "smfSelData", base+"/smf-select-data")
		requireSameAsResource(t, r, sets, "uecSmfData", base+"/ue-context-in-smf-data")
		requireSameAsResource(t, r, sets, "smData", base+"/sm-data")
	}
}

func TestGetDataSetsRepeatedParameter(t *testing.T) {
	r := newDataSetsRouter(t)
	sets := getDataSets(t, r, "/nudm-sdm/v2/"+testSUPI+"?dataset-names=AM&dataset-names=SMF_SEL")
	if _, ok := sets["amData"]; !ok {
		t.Fatalf("amData missing: %v", keys(sets))
	}
	if _, ok := sets["smfSelData"]; !ok {
		t.Fatalf("smfSelData missing: %v", keys(sets))
	}
}

func TestGetDataSetsSMFilters(t *testing.T) {
	r := newDataSetsRouter(t)
	base := "/nudm-sdm/v2/" + testSUPI
	nssai := url.QueryEscape(`{"sst":1}`)

	sets := getDataSets(t, r, base+"?dataset-names=SM,AM&dnn=internet&single-nssai="+nssai)
	requireSameAsResource(t, r, sets, "smData", base+"/sm-data?dnn=internet&single-nssai="+nssai)

	var items []smDataItem
	if err := json.Unmarshal(sets["smData"], &items); err != nil {
		t.Fatalf("decode smData: %v", err)
	}
	if len(items) != 1 || len(items[0].DNNConfigs) != 1 || items[0].DNNConfigs["internet"] == nil {
		t.Fatalf("expected one slice with the internet DNN, got %s", sets["smData"])
	}

	// A DNN the subscriber does not have yields no DNN configurations.
	sets = getDataSets(t, r, base+"?dataset-names=SM,AM&dnn=nope")
	items = nil
	if err := json.Unmarshal(sets["smData"], &items); err != nil {
		t.Fatalf("decode smData: %v", err)
	}
	for _, it := range items {
		if len(it.DNNConfigs) != 0 {
			t.Fatalf("expected no DNN configs for unknown dnn, got %s", sets["smData"])
		}
	}
}

func TestGetDataSetsSMFSelPresentWithoutAPNs(t *testing.T) {
	r := newDataSetsRouter(t)
	sets := getDataSets(t, r, "/nudm-sdm/v2/imsi-001010000000002?dataset-names=AM,SMF_SEL")
	if _, ok := sets["amData"]; !ok {
		t.Fatalf("amData missing: %v", keys(sets))
	}
	if _, ok := sets["smfSelData"]; !ok {
		t.Fatalf("smfSelData must be present even without APNs (Open5GS AMF requires it): %v", keys(sets))
	}
}

func TestGetDataSetsErrors(t *testing.T) {
	r := newDataSetsRouter(t)
	tests := []struct {
		name   string
		path   string
		status int
		cause  string
	}{
		{"missing dataset-names", "/nudm-sdm/v2/" + testSUPI, http.StatusBadRequest, "MANDATORY_QUERY_PARAM_MISSING"},
		{"single name", "/nudm-sdm/v2/" + testSUPI + "?dataset-names=AM", http.StatusBadRequest, "MANDATORY_QUERY_PARAM_INCORRECT"},
		{"duplicate name", "/nudm-sdm/v2/" + testSUPI + "?dataset-names=AM,AM", http.StatusBadRequest, "MANDATORY_QUERY_PARAM_INCORRECT"},
		{"empty name", "/nudm-sdm/v2/" + testSUPI + "?dataset-names=AM,,SM", http.StatusBadRequest, "MANDATORY_QUERY_PARAM_INCORRECT"},
		{"empty value", "/nudm-sdm/v2/" + testSUPI + "?dataset-names=", http.StatusBadRequest, "MANDATORY_QUERY_PARAM_INCORRECT"},
		{"invalid supi", "/nudm-sdm/v2/bogus?dataset-names=AM,SMF_SEL", http.StatusBadRequest, "MANDATORY_IE_INCORRECT"},
		{"unknown subscriber", "/nudm-sdm/v2/imsi-001019999999999?dataset-names=AM,SMF_SEL", http.StatusNotFound, "USER_NOT_FOUND"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doGet(t, r, tt.path)
			if rec.Code != tt.status {
				t.Fatalf("status: got %d want %d body=%s", rec.Code, tt.status, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("Content-Type: got %q", ct)
			}
			var pd problemDetails
			if err := json.Unmarshal(rec.Body.Bytes(), &pd); err != nil {
				t.Fatalf("decode ProblemDetails: %v body=%s", err, rec.Body.String())
			}
			if pd.Status != tt.status || pd.Cause != tt.cause {
				t.Fatalf("ProblemDetails: got %+v, want status %d cause %s", pd, tt.status, tt.cause)
			}
		})
	}
}
