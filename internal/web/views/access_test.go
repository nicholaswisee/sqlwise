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

func TestAdminUserFormsUseFrozenRoutesNativePOSTAndCSRF(t *testing.T) {
	csrf := "csrf-test"
	doc := renderHTML(t, AdminUsersFragment(AccessPageData{CSRFToken: csrf, Users: []AccessUser{{ID: "u1", Username: "mira", Active: true, IsPlatformAdmin: true}, {ID: "u2", Username: "inactive", Active: false}}}))
	wants := map[string]bool{"/admin/users": false, "/admin/users/u1": false, "/admin/users/u1/active": false, "/admin/users/u1/admin": false, "/admin/users/u2": false, "/admin/users/u2/active": false, "/admin/users/u2/admin": false}
	for _, form := range descendants(doc, "form") {
		action := attr(form, "action")
		if _, ok := wants[action]; !ok {
			t.Errorf("unexpected form route %q", action)
			continue
		}
		wants[action] = true
		if attr(form, "method") != "post" {
			t.Errorf("%s is not native POST", action)
		}
		if attr(form, "hx-post") != action {
			t.Errorf("%s has no matching HTMX action", action)
		}
		found := false
		for _, input := range descendants(form, "input") {
			if attr(input, "name") == "csrf_token" && attr(input, "value") == csrf {
				found = true
			}
		}
		if !found {
			t.Errorf("%s lacks hidden CSRF token", action)
		}
	}
	for route, found := range wants {
		if !found {
			t.Errorf("missing form route %s", route)
		}
	}
	if !hasPostField(doc, "/admin/users/u1/active", "active", "false") || !hasPostField(doc, "/admin/users/u2/active", "active", "true") {
		t.Fatal("active state actions do not support disable and re-enable")
	}
	if !hasPostField(doc, "/admin/users/u1/admin", "admin", "false") || !hasPostField(doc, "/admin/users/u2/admin", "admin", "true") {
		t.Fatal("platform admin controls do not support demotion and promotion")
	}
	passwordForms := 0
	for _, form := range descendants(doc, "form") {
		if attr(form, "action") != "/admin/users/u1" {
			continue
		}
		for _, input := range descendants(form, "input") {
			if attr(input, "name") != "password" {
				continue
			}
			passwordForms++
			if attr(input, "type") != "password" || attr(input, "minlength") != "12" || attr(input, "value") != "" {
				t.Error("password reset control must be blank, password typed and require at least 12 characters")
			}
		}
	}
	if passwordForms != 1 {
		t.Fatalf("got %d password reset controls, want one", passwordForms)
	}
	if strings.Contains(textContent(doc), "csrf-test") {
		t.Fatal("CSRF token leaked into visible text")
	}
}

func TestAdminGroupFormsRenderMembershipAndGrantControls(t *testing.T) {
	data := AccessPageData{CSRFToken: "csrf", SelectedGroup: AccessGroup{ID: "g1", Name: "analysts"}, Users: []AccessUser{{ID: "u1", Username: "mira", Active: true}}, Databases: []AccessDatabase{{ID: "db1", Name: "warehouse", Active: true}}, Memberships: []AccessMembership{{UserID: "u1", Username: "mira"}}, Grants: []AccessGrant{{DatabaseID: "db1", DatabaseName: "warehouse", Permission: "view"}}}
	members := renderHTML(t, AdminGroupMembersFragment(data))
	if !hasPost(members, "/admin/groups/g1/members", "user_id", "") || !hasPost(members, "/admin/groups/g1/members/remove", "user_id", "u1") {
		t.Fatal("membership add/remove forms missing")
	}
	databases := renderHTML(t, AdminGroupDatabasesFragment(data))
	if !hasPost(databases, "/admin/groups/g1/databases", "database_id", "") || !hasPost(databases, "/admin/groups/g1/databases/remove", "database_id", "db1") {
		t.Fatal("grant set/remove forms missing")
	}
	if !strings.Contains(textContent(databases), "warehouse") {
		t.Fatal("registered database display name missing")
	}
}

func TestAdminGroupsCanDisableAndReEnable(t *testing.T) {
	doc := renderHTML(t, AdminGroupsFragment(AccessPageData{CSRFToken: "csrf", Groups: []AccessGroup{{ID: "active", Name: "active group", Active: true}, {ID: "inactive", Name: "inactive group", Active: false}}}))
	if !hasPostField(doc, "/admin/groups/active/active", "active", "false") || !hasPostField(doc, "/admin/groups/inactive/active", "active", "true") {
		t.Fatal("active state actions do not support disable and re-enable")
	}
}

func TestAccessPagesShowEscapedFeedbackEmptyAndFilteredAccount(t *testing.T) {
	doc := renderHTML(t, AccessUsers(Assets{}, AccessPageData{Error: "bad <input>", Users: []AccessUser{{ID: "u", Username: `<img src=x onerror=alert(1)>`, Active: false}}}))
	if countRole(doc, "alert") != 1 || !strings.Contains(textContent(doc), `<img src=x onerror=alert(1)>`) {
		t.Fatal("error or escaped username missing")
	}
	if len(descendants(doc, "img")) != 0 {
		t.Fatal("untrusted username rendered as markup")
	}
	empty := renderHTML(t, AccessGroupDatabases(Assets{}, AccessPageData{SelectedGroup: AccessGroup{ID: "g", Name: "empty"}, Databases: []AccessDatabase{{ID: "inactive", Name: "former target", Active: false}}, Grants: []AccessGrant{{DatabaseID: "inactive", DatabaseName: "former target", Permission: "view"}}}))
	if !strings.Contains(textContent(empty), "No active registered databases") || !strings.Contains(textContent(empty), "former target") || !strings.Contains(textContent(empty), "view") {
		t.Fatal("inactive database empty state or existing grant missing")
	}
	for _, option := range descendants(empty, "option") {
		if attr(option, "value") == "inactive" {
			t.Fatal("inactive database was offered for a new grant")
		}
	}
	account := renderHTML(t, AccessAccount(Assets{}, AccessAccountData{Username: "mira", CSRFToken: "account-csrf", IsPlatformAdmin: true, Databases: []AuthorizedDatabase{{Name: "warehouse", Permission: "View"}}}))
	if !hasLink(account, "/admin") || !hasPost(account, "/logout", "csrf_token", "account-csrf") {
		t.Fatal("account admin navigation/logout missing")
	}
	if !strings.Contains(textContent(account), "warehouse") || !strings.Contains(textContent(account), "View") {
		t.Fatal("authorized database missing")
	}
	nonAdmin := renderHTML(t, AccessAccount(Assets{}, AccessAccountData{Username: "mira"}))
	if hasLink(nonAdmin, "/admin") || !strings.Contains(textContent(nonAdmin), "No database access") {
		t.Fatal("non-admin account visibility or empty state incorrect")
	}
}

func TestAdminRouteLinksAndStatusAreAccessible(t *testing.T) {
	doc := renderHTML(t, AccessDashboard(Assets{}, AccessPageData{Message: "Saved", Databases: []AccessDatabase{{ID: "db", Name: "Warehouse", Active: true}}}))
	for _, href := range []string{"/admin", "/admin/users", "/admin/groups", "/account"} {
		if !hasLink(doc, href) {
			t.Errorf("missing navigation link %s", href)
		}
	}
	if hasLink(doc, "/admin/databases") || !strings.Contains(textContent(doc), "Connection management unavailable") {
		t.Fatal("unavailable connection management is linked or not identified")
	}
	if countRole(doc, "status") < 2 || !strings.Contains(textContent(doc), "Warehouse") {
		t.Fatal("dashboard feedback or database list missing")
	}
	available := renderHTML(t, AccessDashboard(Assets{}, AccessPageData{ConnectionsAvailable: true}))
	if !hasLink(available, "/admin/databases") {
		t.Fatal("available connection management route has no navigation link")
	}
}

func countRole(doc *html.Node, role string) int {
	count := 0
	walkHTML(doc, func(n *html.Node) {
		if attr(n, "role") == role {
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
			if attr(input, "name") == name && (value == "" || attr(input, "value") == value) {
				return true
			}
		}
		for _, selectNode := range descendants(form, "select") {
			if attr(selectNode, "name") == name && value == "" {
				return true
			}
		}
	}
	return false
}

func hasPostField(doc *html.Node, action, name, value string) bool {
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
