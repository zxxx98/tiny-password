package vault

import (
	"encoding/json"
	"os"
	"testing"
)

// fixture mirrors tests/fixtures/permissions.json — the authoritative
// expectation matrix, authored from the design rules independently of the
// policy implementation.
type fixture struct {
	Matrix []struct {
		Role     string `json:"role"`
		Scope    string `json:"scope"`
		Relation string `json:"relation"`
		Action   string `json:"action"`
		Allowed  bool   `json:"allowed"`
	} `json:"matrix"`
	Create []struct {
		Role    string `json:"role"`
		Scope   string `json:"scope"`
		Claim   string `json:"claim"`
		Allowed bool   `json:"allowed"`
	} `json:"create"`
	Reference []struct {
		Source      string `json:"source"`
		Target      string `json:"target"`
		TargetOwner string `json:"target_owner"`
		Allowed     bool   `json:"allowed"`
	} `json:"reference"`
	ImmutableOwnership []struct {
		Change  string `json:"change"`
		Allowed bool   `json:"allowed"`
	} `json:"immutable_ownership"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	raw, err := os.ReadFile("../../tests/fixtures/permissions.json")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(f.Matrix) < 80 {
		t.Fatalf("fixture matrix too small: %d rows", len(f.Matrix))
	}
	return f
}

func itemFor(scope, relation, actor string) Item {
	if scope == "personal" {
		owner := "someone-else"
		if relation == "self" {
			owner = actor
		}
		return Item{Scope: ScopePersonal, OwnerID: owner}
	}
	creator := "someone-else"
	if relation == "self" {
		creator = actor
	}
	return Item{Scope: ScopeShared, CreatorID: creator}
}

// TestPolicyMatrix walks every fixture row: both roles, both scopes, self and
// other, and all ten item actions. No combination may lack an expectation.
func TestPolicyMatrix(t *testing.T) {
	f := loadFixture(t)
	actor := "actor-id-1"
	for _, row := range f.Matrix {
		row := row
		t.Run(row.Role+"/"+row.Scope+"/"+row.Relation+"/"+row.Action, func(t *testing.T) {
			it := itemFor(row.Scope, row.Relation, actor)
			got := Can(Role(row.Role), actor, it, Action(row.Action))
			if got != row.Allowed {
				t.Fatalf("Can(%s, actor, %s/%s, %s)=%v, want %v",
					row.Role, row.Scope, row.Relation, row.Action, got, row.Allowed)
			}
		})
	}
}

func TestPolicyCreate(t *testing.T) {
	f := loadFixture(t)
	actor := "actor-id-1"
	other := "someone-else"
	for _, row := range f.Create {
		row := row
		t.Run(row.Role+"/"+row.Scope+"/"+row.Claim, func(t *testing.T) {
			owner, creator := "", ""
			if row.Scope == "personal" {
				owner = actor
				if row.Claim == "foreign" {
					owner = other
				}
			} else {
				creator = actor
				if row.Claim == "foreign" {
					creator = other
				}
			}
			got := CanCreate(Role(row.Role), actor, Scope(row.Scope), owner, creator)
			if got != row.Allowed {
				t.Fatalf("CanCreate(%s, %s, claim=%s)=%v, want %v", row.Role, row.Scope, row.Claim, got, row.Allowed)
			}
		})
	}
}

func TestPolicyReference(t *testing.T) {
	f := loadFixture(t)
	actor := "actor-id-1"
	sourceFor := func(scope string) Item {
		if scope == "personal" {
			return Item{Scope: ScopePersonal, OwnerID: actor}
		}
		return Item{Scope: ScopeShared, CreatorID: actor}
	}
	targetFor := func(scope, owner string) Item {
		holder := actor
		if owner == "other" {
			holder = "someone-else"
		}
		if scope == "personal" {
			return Item{Scope: ScopePersonal, OwnerID: holder}
		}
		return Item{Scope: ScopeShared, CreatorID: holder}
	}
	for _, row := range f.Reference {
		row := row
		t.Run(row.Source+"->"+row.Target+"/"+row.TargetOwner, func(t *testing.T) {
			got := CanReference(actor, sourceFor(row.Source), targetFor(row.Target, row.TargetOwner))
			if got != row.Allowed {
				t.Fatalf("CanReference(%s->%s/%s)=%v, want %v", row.Source, row.Target, row.TargetOwner, got, row.Allowed)
			}
		})
	}
}

func TestPolicyImmutableOwnership(t *testing.T) {
	f := loadFixture(t)
	current := Item{Scope: ScopePersonal, OwnerID: "actor-id-1"}
	personal := ScopePersonal
	shared := ScopeShared
	actor := "actor-id-1"
	other := "someone-else"
	for _, row := range f.ImmutableOwnership {
		row := row
		t.Run(row.Change, func(t *testing.T) {
			var scope *Scope
			var owner, creator *string
			switch row.Change {
			case "scope":
				scope = &shared
			case "owner":
				owner = &other
			case "creator":
				creator = &other
			case "none":
				scope, owner = &personal, &actor
			}
			err := ValidateImmutableOwnership(current, scope, owner, creator)
			if row.Allowed && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !row.Allowed && err == nil {
				t.Fatal("ownership change must be rejected")
			}
		})
	}
}

// TestPolicyAdminHasNoPersonalPrivilege states the flagship guarantee
// explicitly: an administrator is exactly a member where vault items are
// concerned, so the acceptance matrix cannot hide behind role dispatch.
func TestPolicyAdminHasNoPersonalPrivilege(t *testing.T) {
	otherPersonal := Item{Scope: ScopePersonal, OwnerID: "member-1"}
	for _, action := range []Action{ActionRead, ActionUpdate, ActionDelete, ActionRestore, ActionPurge, ActionHistory, ActionHistoryRestore, ActionFavorite, ActionTag, ActionExport} {
		if Can(RoleAdmin, "admin-1", otherPersonal, action) {
			t.Fatalf("admin must not %s another member's personal item", action)
		}
	}
	otherShared := Item{Scope: ScopeShared, CreatorID: "member-1"}
	for _, action := range []Action{ActionUpdate, ActionDelete, ActionRestore, ActionPurge, ActionFavorite, ActionTag, ActionHistoryRestore, ActionExport} {
		if Can(RoleAdmin, "admin-1", otherShared, action) {
			t.Fatalf("admin must not %s another member's shared item", action)
		}
	}
	// Reading shared items is the one cross-user right.
	if !Can(RoleAdmin, "admin-1", otherShared, ActionRead) || !Can(RoleAdmin, "admin-1", otherShared, ActionHistory) {
		t.Fatal("shared items must be readable by admins as by members")
	}
	// Empty actors are always denied, whatever the role.
	if Can(RoleAdmin, "", otherShared, ActionRead) || CanCreate(RoleMember, "", ScopePersonal, "", "") {
		t.Fatal("unidentified actors must be denied")
	}
}
