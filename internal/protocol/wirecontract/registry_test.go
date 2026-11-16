package wirecontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Prefixes and engine-derived suffixes are not independent operation kinds.
// Keep this explicit so a newly added constant cannot silently escape coverage.
var fragments = map[string]bool{
	"OpCodexGoalPrefix": true, "OpDeployContainerPrefix": true, "OpDeployPrefix": true,
	"OpTurnStartSuffix": true, "OpTurnRewriteSuffix": true,
}

func TestRegistryCoversWireConstants(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../kinds.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string][]string{"operation": {}, "upstream": {}}
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			category := ""
			if strings.HasPrefix(name.Name, "Op") && !fragments[name.Name] {
				category = "operation"
			}
			if strings.HasPrefix(name.Name, "AgentKind") {
				category = "upstream"
			}
			if category == "" {
				continue
			}
			literal, ok := spec.Values[i].(*ast.BasicLit)
			if !ok {
				t.Fatalf("kind %s must have a literal value", name.Name)
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			expected[category] = append(expected[category], value)
		}
		return true
	})
	for category, actual := range map[string][]string{"operation": OperationKinds(), "upstream": UpstreamKinds()} {
		sort.Strings(expected[category])
		if !reflect.DeepEqual(expected[category], actual) {
			t.Fatalf("%s registry mismatch\nwant %v\ngot  %v", category, expected[category], actual)
		}
	}
	for _, kind := range OperationKinds() {
		c, ok := Operation(kind)
		if !ok || c.Payload == nil {
			t.Fatalf("missing payload for %s", kind)
		}
	}
	for _, kind := range UpstreamKinds() {
		c, ok := Upstream(kind)
		if !ok || c.Payload == nil || c.Result != nil {
			t.Fatalf("invalid upstream contract %s", kind)
		}
	}
	if _, ok := Operation("unknown.operation"); ok {
		t.Fatal("unknown operation registered")
	}
	if _, ok := Upstream("unknown_envelope"); ok {
		t.Fatal("unknown envelope registered")
	}
}

type fixture struct {
	Baseline  string          `json:"baseline"`
	Direction string          `json:"direction"`
	Kind      string          `json:"kind"`
	Source    []string        `json:"source"`
	Payload   json.RawMessage `json:"payload"`
}

func strictDecode(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("multiple JSON values")
	}
	return nil
}
func semantic(t *testing.T, raw []byte) any {
	t.Helper()
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestImmutableWireFixtures(t *testing.T) {
	paths, err := filepath.Glob("../testdata/wire/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("missing fixtures: %v", err)
	}
	covered := map[string]map[string]bool{"operation": {}, "upstream": {}}
	resultTypes := map[reflect.Type]bool{}
	fieldsSeen := map[reflect.Type]map[string]bool{}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var f fixture
			if err := strictDecode(raw, &f); err != nil {
				t.Fatal(err)
			}
			if f.Baseline == "" || len(f.Source) == 0 {
				t.Fatal("fixture lacks provenance")
			}
			c, ok := Operation(f.Kind)
			if f.Direction == "upstream" {
				c, ok = Upstream(f.Kind)
			}
			if !ok {
				t.Fatalf("unregistered kind %q", f.Kind)
			}
			factory := c.Payload
			switch f.Direction {
			case "operation", "upstream":
				covered[f.Direction][f.Kind] = true
			case "result":
				factory = c.Result
			default:
				t.Fatalf("unknown direction %q", f.Direction)
			}
			if factory == nil {
				t.Fatalf("%s has no typed %s", f.Kind, f.Direction)
			}
			value := factory()
			typ := reflect.TypeOf(value).Elem()
			if fieldsSeen[typ] == nil {
				fieldsSeen[typ] = map[string]bool{}
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(f.Payload, &fields); err != nil {
				t.Fatal(err)
			}
			for name, raw := range fields {
				if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
					fieldsSeen[typ][name] = true
				}
			}
			if err := strictDecode(f.Payload, value); err != nil {
				t.Fatalf("strict payload decode: %v", err)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(semantic(t, f.Payload), semantic(t, encoded)) {
				t.Fatalf("wire content changed\nfixture %s\nencoded %s", f.Payload, encoded)
			}
			if f.Direction == "result" {
				resultTypes[reflect.TypeOf(value)] = true
			}
		})
	}
	assertOptionalFields(t, fieldsSeen)
	for _, kind := range OperationKinds() {
		if !covered["operation"][kind] {
			t.Errorf("missing operation fixture %s", kind)
		}
		c, _ := Operation(kind)
		if c.Result != nil && !resultTypes[reflect.TypeOf(c.Result())] {
			t.Errorf("missing result fixture for %s (%T)", kind, c.Result())
		}
	}
	for _, kind := range UpstreamKinds() {
		if !covered["upstream"][kind] {
			t.Errorf("missing upstream fixture %s", kind)
		}
	}
}

// Every optional top-level wire field must have a populated variant. This also
// makes adding a DTO field require an append-only fixture addition.
func assertOptionalFields(t *testing.T, fieldsSeen map[reflect.Type]map[string]bool) {
	t.Helper()
	for typ, fields := range fieldsSeen {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			tag := field.Tag.Get("json")
			if strings.Contains(tag, ",omitempty") && !fields[strings.Split(tag, ",")[0]] {
				t.Errorf("missing populated optional field %s.%s", typ.Name(), field.Name)
			}
		}
	}
}
