package analysis

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

func TestBuilder_RecordDefinedItem_CreatesRecordsForDeps(t *testing.T) {
	targetLabel := environment.Label{Dir: mustDir(t, "//"), Name: "main_target"}
	dep1Label := environment.Label{Dir: mustDir(t, "//"), Name: "dep_one"}
	dep2Label := environment.Label{Dir: mustDir(t, "//"), Name: "dep_two"}
	target := &Target{
		itemInfo: itemInfo{
			label:       targetLabel,
			definedFrom: &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "main_target")},
		},
		targetType: "executable",
		privateDeps: []LabelTargetPair{
			{
				Label:  dep1Label,
				Origin: &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenString, ":dep_one")},
			},
			{
				Label:  dep2Label,
				Origin: &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenString, ":dep_two")},
			},
		},
	}
	wantUnresolvedDeps := []environment.LabelWithOrigin{
		{
			Label:  dep1Label,
			Origin: target.privateDeps[0].Origin,
		},
		{
			Label:  dep2Label,
			Origin: target.privateDeps[1].Origin,
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
		t.Error("no record for //:dep_one created")
	} else if dep1Rec.state != itemStateUndefined {
		t.Errorf("record //:dep_one state=%v; want itemStateUndefined as Target not seen", dep1Rec.state)
	}
	dep2Rec, ok := builder.records[dep2Label]
	if !ok {
		t.Error("no record for //:dep_two created")
	} else if dep2Rec.state != itemStateUndefined {
		t.Errorf("record //:dep_two state=%v; want itemStateUndefined as Target not seen", dep2Rec.state)
	}
	if _, isDep := targetRec.dependencies[dep1Rec]; !isDep {
		t.Error("record //:main_target missing edge to //:dep_one")
	}
	if _, isDep := targetRec.dependencies[dep2Rec]; !isDep {
		t.Error("record //:main_target missing edge to //:dep_two")
	}
	if targetRec.unresolvedDeps != 2 {
		t.Errorf("record //:main_target unresolvedDeps=%d; want 2", targetRec.unresolvedDeps)
	}
}

func TestBuilder_ItemTypeMismatch(t *testing.T) {
	configLabel := environment.Label{Dir: mustDir(t, "//"), Name: "foo_config"}
	targetLabel := environment.Label{Dir: mustDir(t, "//"), Name: "foo_target"}
	builder := MakeBuilder(nil)
	cfgItem := &Config{
		itemInfo: itemInfo{
			label:       configLabel,
			definedFrom: &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "foo_config")},
		},
	}
	targetItem := &Target{
		itemInfo: itemInfo{
			label:       targetLabel,
			definedFrom: &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "foo_target")},
		},
		targetType: "executable",
		privateDeps: []LabelTargetPair{
			{
				Label:  configLabel,
				Origin: &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenString, ":foo_config")},
			},
		},
	}

	if _, err := builder.RecordDefinedItem(cfgItem); err != nil {
		t.Fatalf("Failed to define config: %v", err)
	}
	_, err := builder.RecordDefinedItem(targetItem)

	if err == nil {
		t.Errorf("RecordDefinedItem(_)=_, ok; want err")
	}
	var typeErr ItemTypeMismatchError
	if !errors.As(err, &typeErr) {
		t.Errorf("RecordDefinedItem(_)=_, err type %T; want %T", err, typeErr)
	}
}
