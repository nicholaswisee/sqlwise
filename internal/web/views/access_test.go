package views

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func renderHTML(t *testing.T, component interface {
	Render(context.Context, io.Writer) error
}) *html.Node {
	t.Helper()
	var output bytes.Buffer
	if err := component.Render(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	doc, err := html.Parse(strings.NewReader(output.String()))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func walkHTML(node *html.Node, visit func(*html.Node)) {
	visit(node)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, visit)
	}
}

func attr(node *html.Node, name string) string {
	for _, a := range node.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

func textContent(node *html.Node) string {
	var text strings.Builder
	walkHTML(node, func(n *html.Node) {
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
		}
	})
	return text.String()
}

func descendants(node *html.Node, tag string) []*html.Node {
	var found []*html.Node
	walkHTML(node, func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == tag {
			found = append(found, n)
		}
	})
	return found
}

func TestAccessAdminRendersScopedActionsAndCSRF(t *testing.T) {
	csrf := "csrf-token-for-tests"
	doc := renderHTML(t, AccessAdmin(Assets{Dev: true}, AccessAdminData{
		CSRFToken:   csrf,
		Username:    "admin",
		Users:       []AccessUser{{ID: "user-1", Username: "mira", Active: false}},
		Groups:      []AccessGroup{{ID: "group-1", Name: "analysts"}},
		Databases:   []AccessDatabase{{ID: "db-1", Name: "warehouse"}},
		Memberships: []AccessMembership{{UserID: "user-1", GroupID: "group-1", Username: "mira", GroupName: "analysts"}},
		Grants:      []AccessGrant{{GroupID: "group-1", DatabaseID: "db-1", GroupName: "analysts", DatabaseName: "warehouse", Permission: "edit"}},
	}))
	wantActions := map[string]map[string]bool{
		"/admin/access/users":              {"username": true, "password": true, "csrf_token": true},
		"/admin/access/groups":             {"name": true, "csrf_token": true},
		"/admin/access/memberships":        {"user_id": true, "group_id": true, "csrf_token": true},
		"/admin/access/memberships/remove": {"user_id": true, "group_id": true, "csrf_token": true},
		"/admin/access/grants":             {"group_id": true, "database_id": true, "permission": true, "csrf_token": true},
		"/admin/access/grants/remove":      {"group_id": true, "database_id": true, "csrf_token": true},
		"/admin/access/revoke-sessions":    {"user_id": true, "csrf_token": true},
	}
	forms := descendants(doc, "form")
	if len(forms) != len(wantActions) {
		t.Fatalf("got %d forms, want %d", len(forms), len(wantActions))
	}
	for _, form := range forms {
		action := attr(form, "action")
		wantFields, ok := wantActions[action]
		if !ok {
			t.Errorf("unexpected form action %q", action)
			continue
		}
		if attr(form, "method") != "post" {
			t.Errorf("%s method = %q, want post", action, attr(form, "method"))
		}
		gotFields := map[string]bool{}
		for _, input := range descendants(form, "input") {
			name := attr(input, "name")
			gotFields[name] = true
			if name == "csrf_token" {
				if attr(input, "type") != "hidden" {
					t.Errorf("%s CSRF field is not hidden", action)
				}
				if attr(input, "value") != csrf {
					t.Errorf("%s has wrong CSRF token", action)
				}
			}
			if name == "password" && attr(input, "value") != "" {
				t.Errorf("password is rendered into a value attribute")
			}
		}
		for _, selectNode := range descendants(form, "select") {
			gotFields[attr(selectNode, "name")] = true
		}
		for field := range wantFields {
			if !gotFields[field] {
				t.Errorf("%s is missing field %q", action, field)
			}
		}
		for field := range gotFields {
			if !wantFields[field] {
				t.Errorf("%s has unexpected field %q", action, field)
			}
		}
		delete(wantActions, action)
	}
	for action := range wantActions {
		t.Errorf("missing form action %q", action)
	}
	for _, label := range descendants(doc, "label") {
		id := attr(label, "for")
		if id == "" {
			t.Errorf("label has no control association: %q", textContent(label))
			continue
		}
		found := false
		walkHTML(doc, func(node *html.Node) {
			if (node.Data == "input" || node.Data == "select") && attr(node, "id") == id {
				found = true
			}
		})
		if !found {
			t.Errorf("label %q does not identify a control", textContent(label))
		}
	}
	for _, status := range descendants(doc, "p") {
		if attr(status, "role") == "status" && textContent(status) != "" {
			t.Errorf("unexpected status in empty-message rendering: %q", textContent(status))
		}
	}
	if got := textContent(doc); !strings.Contains(got, "mira") || !strings.Contains(got, "analysts") || !strings.Contains(got, "warehouse") {
		t.Fatalf("existing relationships are not shown with names: %q", got)
	}
	if !strings.Contains(textContent(doc), "Inactive") {
		t.Fatal("inactive user state is not visible")
	}
}

func TestAccessAdminEscapesNamesAndUsesAccessibleStatusAndAlert(t *testing.T) {
	doc := renderHTML(t, AccessAdmin(Assets{}, AccessAdminData{
		CSRFToken: "csrf-token-value",
		Username:  `<script>alert("x")</script>`,
		Message:   `Saved <access>`,
		Error:     `Could not save <access>`,
		Users:     []AccessUser{{ID: "u", Username: `<img src=x onerror=alert(1)>`, Active: true}},
	}))
	if got := textContent(doc); !strings.Contains(got, `<script>alert("x")</script>`) || !strings.Contains(got, `<img src=x onerror=alert(1)>`) {
		t.Fatalf("expected user-provided text to appear as text, got %q", got)
	}
	if countRole(doc, "status") != 1 || countRole(doc, "alert") != 1 {
		t.Fatalf("message and error should use status/alert roles")
	}
}

func TestAccessAccountShowsOnlyAuthorizedTargetsAndAdminLink(t *testing.T) {
	doc := renderHTML(t, AccessAccount(Assets{}, AccessAccountData{
		Username: "mira", CSRFToken: "csrf-account", IsPlatformAdmin: true,
		Databases: []AuthorizedDatabase{{ID: "db-1", Name: "warehouse", Permission: "View"}},
	}))
	text := textContent(doc)
	if !strings.Contains(text, "mira") || !strings.Contains(text, "warehouse") || !strings.Contains(text, "View") {
		t.Fatalf("authorized account access missing from page: %q", text)
	}
	if !hasLink(doc, "/admin/access") {
		t.Fatal("platform admin has no access-control link")
	}
	if !hasPost(doc, "/logout", "csrf_token", "csrf-account") {
		t.Fatal("account page is missing native CSRF-protected logout")
	}
	if strings.Contains(text, "No access") {
		t.Fatalf("account page should not list unauthorized targets: %q", text)
	}
}

func TestAccessAccountNonAdminAndEmptyState(t *testing.T) {
	doc := renderHTML(t, AccessAccount(Assets{}, AccessAccountData{Username: "mira"}))
	if hasLink(doc, "/admin/access") {
		t.Fatal("non-admin account has an admin link")
	}
	if !strings.Contains(textContent(doc), "No database access") {
		t.Fatalf("account empty state missing: %q", textContent(doc))
	}
}

func TestAccessAdminDistinguishesAllEmptyCollections(t *testing.T) {
	doc := renderHTML(t, AccessAdmin(Assets{}, AccessAdminData{Username: "admin"}))
	for _, emptyState := range []string{
		"No users yet.",
		"No groups yet.",
		"No registered targets yet.",
		"Connection registration is not available in this slice.",
		"No memberships yet.",
		"No target access grants yet.",
	} {
		if !strings.Contains(textContent(doc), emptyState) {
			t.Errorf("missing distinct empty state %q", emptyState)
		}
	}
	if hasLink(doc, "/viewer") {
		t.Fatal("admin template contains a nonfunctional viewer link")
	}
}

func countRole(doc *html.Node, role string) int {
	count := 0
	walkHTML(doc, func(node *html.Node) {
		if attr(node, "role") == role {
			count++
		}
	})
	return count
}

func hasLink(doc *html.Node, href string) bool {
	for _, link := range descendants(doc, "a") {
		if attr(link, "href") == href {
			return true
		}
	}
	return false
}

func hasPost(doc *html.Node, action, name, value string) bool {
	for _, form := range descendants(doc, "form") {
		if attr(form, "action") != action || attr(form, "method") != "post" {
			continue
		}
		for _, input := range descendants(form, "input") {
			if attr(input, "name") == name && attr(input, "value") == value {
				return true
			}
		}
	}
	return false
}
