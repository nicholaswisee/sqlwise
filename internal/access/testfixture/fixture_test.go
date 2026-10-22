package testfixture_test

import (
	"context"
	"errors"
	"testing"

	"sqlwise/internal/access"
	"sqlwise/internal/access/testfixture"
)

func TestFixtureProvidesDistinctConsumerPermissions(t *testing.T) {
	f := testfixture.New(t)
	for _, scenario := range []struct {
		name     string
		userID   string
		context  context.Context
		targetID string
		want     access.Permission
	}{
		{"admin without grant", f.Admin.UserID, f.Admin.Context, testfixture.PrimaryTargetID, access.None},
		{"no access", f.NoAccess.UserID, f.NoAccess.Context, testfixture.PrimaryTargetID, access.None},
		{"view", f.Viewer.UserID, f.Viewer.Context, testfixture.PrimaryTargetID, access.View},
		{"edit", f.Editor.UserID, f.Editor.Context, testfixture.PrimaryTargetID, access.Edit},
		{"multi group maximum", f.MultiGroup.UserID, f.MultiGroup.Context, testfixture.PrimaryTargetID, access.Edit},
		{"multi group other target", f.MultiGroup.UserID, f.MultiGroup.Context, testfixture.SecondaryTargetID, access.View},
		{"view other target", f.Viewer.UserID, f.Viewer.Context, testfixture.SecondaryTargetID, access.None},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			got, err := f.Access.EffectivePermission(scenario.context, scenario.userID, scenario.targetID)
			if err != nil || got != scenario.want {
				t.Fatalf("permission=%s err=%v want=%s", got, err, scenario.want)
			}
		})
	}
	if err := f.Access.Require(f.Viewer.Context, f.Viewer.UserID, testfixture.PrimaryTargetID, access.Edit); !errors.Is(err, access.ErrPermissionDenied) {
		t.Fatalf("viewer edit error=%v", err)
	}
}
