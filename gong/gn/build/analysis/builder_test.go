package analysis

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

func TestBuilder_RecordDefinedItem_CreatesRecordsForDeps(t *testing.T) {
	targetLabel := environment.Label{Dir: mustDir(t, "//"), Name: "main_target"}
	dep1Label := environment.Label{Dir: mustDir(t, "//bar"), Name: "baz"}
	dep2Label := environment.Label{Dir: mustDir(t, "//bar"), Name: "qux"}
	dep1Origin := &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenString, "//bar:baz")}
	dep2Origin := &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenString, "//bar:qux")}
	target := &Target{
		itemInfo: itemInfo{
			label:       targetLabel,
			definedFrom: &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "main_target")},
		},
		schema: &executableSchema,
		values: map[string]resolve.Value{
			"name": resolve.NewOriginlessStringValue("main_target"),
			"deps": resolve.NewOriginlessListValue([]resolve.Value{
				resolve.NewStringValueAt(dep1Origin, "//bar:baz"),
				resolve.NewStringValueAt(dep2Origin, "//bar:qux"),
			}),
		},
	}
	wantUnresolvedDeps := []environment.LabelWithOrigin{
		{
			Label:  dep1Label,
			Origin: dep1Origin,
		},
		{
			Label:  dep2Label,
			Origin: dep2Origin,
		},
	}

	builder := MakeBuilder(nil)
	unresolvedDeps, err := builder.RecordDefinedItem(target)
	if err != nil {
		t.Fatalf("RecordDefinedItem(_)=_, %v; want nil err", err)
	}

	if diff := cmp.Diff(wantUnresolvedDeps, unresolvedDeps); diff != "" {
		t.Errorf("RecordDefinedItem(_); diff (-want +got):\n%s", diff)
	}
	targetRec, ok := builder.records[targetLabel]
	if !ok {
		t.Fatal("no record for //:main_target created")
	}
	if targetRec.item != target {
		t.Errorf("record //:main_target %v; want %v", targetRec.item, target)
	}
	dep1Rec, ok := builder.records[dep1Label]
	if !ok {
		t.Error("no record for //bar:baz created")
	} else if dep1Rec.state != itemStateUndefined {
		t.Errorf("record //bar:baz state=%v; want itemStateUndefined as Target not seen", dep1Rec.state)
	}
	dep2Rec, ok := builder.records[dep2Label]
	if !ok {
		t.Error("no record for //bar:qux created")
	} else if dep2Rec.state != itemStateUndefined {
		t.Errorf("record //bar:qux state=%v; want itemStateUndefined as Target not seen", dep2Rec.state)
	}
	if _, isDep := targetRec.dependencies[dep1Rec]; !isDep {
		t.Error("record //:main_target missing edge to //bar:baz")
	}
	if _, isDep := targetRec.dependencies[dep2Rec]; !isDep {
		t.Error("record //:main_target missing edge to //bar:qux")
	}
	if targetRec.unresolvedDeps != 2 {
		t.Errorf("record //:main_target unresolvedDeps=%d; want 2", targetRec.unresolvedDeps)
	}
}

func TestBuilder_ItemTypeMismatch(t *testing.T) {
	configLabel := environment.Label{Dir: mustDir(t, "//"), Name: "foo_config"}
	targetLabel := environment.Label{Dir: mustDir(t, "//"), Name: "foo_target"}
	depLabel := environment.Label{Dir: mustDir(t, "//bar"), Name: "baz_target"}
	cfgItem := &Config{
		itemInfo: itemInfo{
			label:       configLabel,
			definedFrom: &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "foo_config")},
		},
	}
	depItem := &Target{
		itemInfo: itemInfo{
			label:       depLabel,
			definedFrom: &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "baz_target")},
		},
		schema: &sharedLibrarySchema,
		settings: &Settings{
			buildSettings: &environment.BuildSettings{
				BuildDir: mustDir(t, "/"),
			},
		},
		values: map[string]resolve.Value{
			"name": resolve.NewOriginlessStringValue("baz_target"),
		},
	}

	for _, tc := range []struct {
		name         string
		targetValues map[string]resolve.Value
	}{
		{
			name: "config in deps",
			targetValues: map[string]resolve.Value{
				"deps": resolve.NewOriginlessListValue([]resolve.Value{
					resolve.NewStringValueAt(cfgItem.definedFrom, "//:foo_config"),
				}),
			},
		},
		{
			name: "target in configs",
			targetValues: map[string]resolve.Value{
				"configs": resolve.NewOriginlessListValue([]resolve.Value{
					resolve.NewStringValueAt(depItem.definedFrom, "//bar:baz_target"),
				}),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			builder := MakeBuilder(nil)
			if _, err := builder.RecordDefinedItem(cfgItem); err != nil {
				t.Fatalf("Failed to define config: %v", err)
			}
			if _, err := builder.RecordDefinedItem(depItem); err != nil {
				t.Fatalf("Failed to define config: %v", err)
			}
			_, err := builder.RecordDefinedItem(&Target{
				itemInfo: itemInfo{
					label:       targetLabel,
					definedFrom: &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "foo_target")},
				},
				schema: &executableSchema,
				values: tc.targetValues,
			})

			if err == nil {
				t.Errorf("RecordDefinedItem(_)=_, ok; want err")
			}
			var typeErr ItemTypeMismatchError
			if !errors.As(err, &typeErr) {
				t.Errorf("RecordDefinedItem(_)=_, err type %T; want %T", err, typeErr)
			}
		})
	}
}
