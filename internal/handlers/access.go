package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/web/views"
)

type AccessAdmin struct {
	auth   *Auth
	policy *access.Service
	assets views.Assets
}

func NewAccessAdmin(authHandler *Auth, policy *access.Service, assets views.Assets) *AccessAdmin {
	return &AccessAdmin{auth: authHandler, policy: policy, assets: assets}
}

// Register installs the administrator console. Every read and mutation first
// validates the session and then rechecks current platform-admin status.
func (h *AccessAdmin) Register(router *gin.Engine) {
	admin := router.Group("", h.auth.RequireSession(), h.auth.RequirePlatformAdmin())
	admin.GET("/admin", h.dashboard)
	admin.GET("/admin/users", h.users)
	admin.GET("/admin/groups", h.groups)
	admin.GET("/admin/groups/:id/members", h.members)
	admin.GET("/admin/groups/:id/databases", h.databases)

	admin.POST("/admin/users", h.createUser)
	admin.POST("/admin/users/:id", h.updateUser)
	admin.POST("/admin/users/:id/disable", h.disableUser)
	admin.POST("/admin/users/:id/active", h.setUserActive)
	admin.POST("/admin/users/:id/admin", h.setPlatformAdmin)
	admin.POST("/admin/groups", h.createGroup)
	admin.POST("/admin/groups/:id", h.updateGroup)
	admin.POST("/admin/groups/:id/disable", h.disableGroup)
	admin.POST("/admin/groups/:id/active", h.setGroupActive)
	admin.POST("/admin/groups/:id/members", h.addMember)
	admin.POST("/admin/groups/:id/members/remove", h.removeMember)
	admin.POST("/admin/groups/:id/databases", h.setGrant)
	admin.POST("/admin/groups/:id/databases/remove", h.removeGrant)
}

func (h *AccessAdmin) dashboard(c *gin.Context) { h.page(c, "dashboard", "", "", "") }
func (h *AccessAdmin) users(c *gin.Context)     { h.page(c, "users", "", "", "") }
func (h *AccessAdmin) groups(c *gin.Context)    { h.page(c, "groups", "", "", "") }
func (h *AccessAdmin) members(c *gin.Context)   { h.page(c, "members", c.Param("id"), "", "") }
func (h *AccessAdmin) databases(c *gin.Context) { h.page(c, "databases", c.Param("id"), "", "") }

func (h *AccessAdmin) page(c *gin.Context, section, groupID, message, problem string) {
	if c.Query("notice") == "saved" {
		message = "Changes saved successfully."
	}
	data, err := h.pageData(c, groupID, message, problem)
	if err != nil {
		if h.boundaryFailure(c, err) {
			return
		}
		if errors.Is(err, access.ErrNotFound) {
			Render(c, http.StatusNotFound, views.AuthError(h.assets, "The requested group was not found."))
			return
		}
		h.auth.failure(c, err)
		return
	}
	fragment := c.GetHeader("HX-Request") == "true"
	// Each route has a distinct page and matching HTMX fragment.
	switch section {
	case "users":
		if fragment {
			Render(c, http.StatusOK, views.AdminUsersFragment(data))
		} else {
			Render(c, http.StatusOK, views.AccessUsers(h.assets, data))
		}
	case "groups":
		if fragment {
			Render(c, http.StatusOK, views.AdminGroupsFragment(data))
		} else {
			Render(c, http.StatusOK, views.AccessGroups(h.assets, data))
		}
	case "members":
		if fragment {
			Render(c, http.StatusOK, views.AdminGroupMembersFragment(data))
		} else {
			Render(c, http.StatusOK, views.AccessGroupMembers(h.assets, data))
		}
	case "databases":
		if fragment {
			Render(c, http.StatusOK, views.AdminGroupDatabasesFragment(data))
		} else {
			Render(c, http.StatusOK, views.AccessGroupDatabases(h.assets, data))
		}
	default:
		if fragment {
			Render(c, http.StatusOK, views.AdminDashboardFragment(data))
		} else {
			Render(c, http.StatusOK, views.AccessDashboard(h.assets, data))
		}
	}
}

func (h *AccessAdmin) pageData(c *gin.Context, groupID, message, problem string) (views.AccessPageData, error) {
	session := c.MustGet("auth_session").(auth.Session)
	snapshot, err := h.policy.AdminSnapshot(c.Request.Context())
	if err != nil {
		return views.AccessPageData{}, err
	}
	return accessPageData(snapshot, session, groupID, message, problem)
}

func accessPageData(snapshot access.AdminSnapshot, session auth.Session, groupID, message, problem string) (views.AccessPageData, error) {
	data := views.AccessPageData{CSRFToken: session.CSRFToken, Username: session.Principal.Username, Message: message, Error: problem, ConnectionsAvailable: true}
	users, groups, databases := map[string]string{}, map[string]string{}, map[string]string{}
	for _, user := range snapshot.Users {
		data.Users = append(data.Users, views.AccessUser{ID: user.ID, Username: user.Username, Active: user.Active, IsPlatformAdmin: user.IsPlatformAdmin})
		users[user.ID] = user.Username
	}
	for _, group := range snapshot.Groups {
		viewGroup := views.AccessGroup{ID: group.ID, Name: group.Name, Active: group.Active}
		data.Groups = append(data.Groups, viewGroup)
		groups[group.ID] = group.Name
		if group.ID == groupID {
			data.SelectedGroup = viewGroup
		}
	}
	for _, database := range snapshot.Databases {
		data.Databases = append(data.Databases, views.AccessDatabase{ID: database.ID, Name: database.Name, Active: database.Active})
		databases[database.ID] = database.Name
	}
	for _, membership := range snapshot.Memberships {
		if groupID != "" && membership.GroupID != groupID {
			continue
		}
		data.Memberships = append(data.Memberships, views.AccessMembership{UserID: membership.UserID, GroupID: membership.GroupID, Username: users[membership.UserID], GroupName: groups[membership.GroupID]})
	}
	for _, grant := range snapshot.Grants {
		if groupID != "" && grant.GroupID != groupID {
			continue
		}
		data.Grants = append(data.Grants, views.AccessGrant{GroupID: grant.GroupID, DatabaseID: grant.DatabaseID, GroupName: groups[grant.GroupID], DatabaseName: databases[grant.DatabaseID], Permission: grant.Permission.String()})
	}
	if groupID != "" && data.SelectedGroup.ID == "" {
		return views.AccessPageData{}, access.ErrNotFound
	}
	return data, nil
}

func (h *AccessAdmin) createUser(c *gin.Context) {
	username, password := c.Request.PostForm.Get("username"), c.Request.PostForm.Get("password")
	credentials, err := auth.ParseCredentials(username, password)
	if err == nil {
		_, err = h.auth.service.CreateUser(c.Request.Context(), credentials)
	}
	h.mutateResult(c, "/admin/users", "User created.", err)
}
func (h *AccessAdmin) updateUser(c *gin.Context) {
	id := c.Param("id")
	username, password, err := userUpdateInput(c.Request.PostForm.Get("username"), c.Request.PostForm.Get("password"))
	if err == nil {
		if username != "" {
			err = h.auth.service.RenameUser(c.Request.Context(), id, username)
		} else {
			err = h.auth.service.UpdateUserPassword(c.Request.Context(), id, password)
		}
	}
	h.mutateResult(c, "/admin/users", "User updated.", err)
}

func userUpdateInput(username, password string) (string, string, error) {
	username = strings.TrimSpace(username)
	if (username == "") == (password == "") {
		return "", "", auth.ErrInvalidInput
	}
	return username, password, nil
}
func (h *AccessAdmin) disableUser(c *gin.Context) {
	h.mutateResult(c, "/admin/users", "User disabled.", h.auth.service.SetUserActive(c.Request.Context(), c.Param("id"), false))
}
func (h *AccessAdmin) setUserActive(c *gin.Context) {
	active, err := parseActive(c.Request.PostForm.Get("active"))
	if err == nil {
		err = h.auth.service.SetUserActive(c.Request.Context(), c.Param("id"), active)
	}
	message := "User disabled."
	if active {
		message = "User enabled."
	}
	h.mutateResult(c, "/admin/users", message, err)
}
func (h *AccessAdmin) setPlatformAdmin(c *gin.Context) {
	admin, err := parseActive(c.Request.PostForm.Get("admin"))
	if err == nil {
		err = h.auth.service.SetPlatformAdmin(c.Request.Context(), c.Param("id"), admin)
	}
	message := "Platform admin removed."
	if admin {
		message = "Platform admin granted."
	}
	h.mutateResult(c, "/admin/users", message, err)
}
func (h *AccessAdmin) createGroup(c *gin.Context) {
	_, err := h.policy.CreateGroup(c.Request.Context(), c.Request.PostForm.Get("name"))
	h.mutateResult(c, "/admin/groups", "Group created.", err)
}
func (h *AccessAdmin) updateGroup(c *gin.Context) {
	h.mutateResult(c, "/admin/groups", "Group updated.", h.policy.RenameGroup(c.Request.Context(), c.Param("id"), c.Request.PostForm.Get("name")))
}
func (h *AccessAdmin) disableGroup(c *gin.Context) {
	h.mutateResult(c, "/admin/groups", "Group disabled.", h.policy.SetGroupActive(c.Request.Context(), c.Param("id"), false))
}
func (h *AccessAdmin) setGroupActive(c *gin.Context) {
	active, err := parseActive(c.Request.PostForm.Get("active"))
	if err == nil {
		err = h.policy.SetGroupActive(c.Request.Context(), c.Param("id"), active)
	}
	message := "Group disabled."
	if active {
		message = "Group enabled."
	}
	h.mutateResult(c, "/admin/groups", message, err)
}
func parseActive(raw string) (bool, error) {
	switch raw {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, access.ErrInvalidInput
	}
}
func (h *AccessAdmin) addMember(c *gin.Context) {
	h.mutateResult(c, "/admin/groups/"+url.PathEscape(c.Param("id"))+"/members", "Membership added.", h.policy.AddMembership(c.Request.Context(), c.Request.PostForm.Get("user_id"), c.Param("id")))
}
func (h *AccessAdmin) removeMember(c *gin.Context) {
	h.mutateResult(c, "/admin/groups/"+url.PathEscape(c.Param("id"))+"/members", "Membership removed.", h.policy.RemoveMembership(c.Request.Context(), c.Request.PostForm.Get("user_id"), c.Param("id")))
}
func (h *AccessAdmin) setGrant(c *gin.Context) {
	permission, err := access.ParsePermission(c.Request.PostForm.Get("permission"))
	if err == nil {
		err = h.policy.SetGrant(c.Request.Context(), c.Param("id"), c.Request.PostForm.Get("database_id"), permission)
	}
	h.mutateResult(c, "/admin/groups/"+url.PathEscape(c.Param("id"))+"/databases", "Access saved.", err)
}
func (h *AccessAdmin) removeGrant(c *gin.Context) {
	h.mutateResult(c, "/admin/groups/"+url.PathEscape(c.Param("id"))+"/databases", "Access removed.", h.policy.RemoveGrant(c.Request.Context(), c.Param("id"), c.Request.PostForm.Get("database_id")))
}

func (h *AccessAdmin) mutateResult(c *gin.Context, location, message string, err error) {
	if h.boundaryFailure(c, err) {
		return
	}
	if err == nil {
		_, evidenceErr := h.auth.service.ValidateContextFresh(c.Request.Context())
		if errors.Is(evidenceErr, auth.ErrAuthorizationEpoch) {
			redirectAfterAdminMutation(c, "/account")
			return
		}
		if errors.Is(evidenceErr, auth.ErrSession) {
			redirectAfterAdminMutation(c, "/login")
			return
		}
	}
	section, groupID := adminSection(location)
	if err == nil && c.GetHeader("HX-Request") != "true" {
		separator := "?"
		if strings.Contains(location, "?") {
			separator = "&"
		}
		c.Redirect(http.StatusSeeOther, location+separator+"notice=saved")
		return
	}
	status, message, problem := adminFeedback(message, err)
	data, loadErr := h.pageData(c, groupID, message, problem)
	if loadErr != nil {
		if h.boundaryFailure(c, loadErr) {
			return
		}
		if errors.Is(loadErr, access.ErrNotFound) {
			Render(c, http.StatusNotFound, views.AuthError(h.assets, "The requested group was not found."))
			return
		}
		h.auth.failure(c, loadErr)
		return
	}
	if c.GetHeader("HX-Request") == "true" {
		switch section {
		case "users":
			Render(c, status, views.AdminUsersFragment(data))
		case "groups":
			Render(c, status, views.AdminGroupsFragment(data))
		case "members":
			Render(c, status, views.AdminGroupMembersFragment(data))
		case "databases":
			Render(c, status, views.AdminGroupDatabasesFragment(data))
		default:
			Render(c, status, views.AdminDashboardFragment(data))
		}
		return
	}
	switch section {
	case "users":
		Render(c, status, views.AccessUsers(h.assets, data))
	case "groups":
		Render(c, status, views.AccessGroups(h.assets, data))
	case "members":
		Render(c, status, views.AccessGroupMembers(h.assets, data))
	case "databases":
		Render(c, status, views.AccessGroupDatabases(h.assets, data))
	default:
		Render(c, status, views.AccessDashboard(h.assets, data))
	}
}

func redirectAfterAdminMutation(c *gin.Context, location string) {
	if c.GetHeader("HX-Request") == "true" {
		c.Header("HX-Redirect", location)
		c.Status(http.StatusNoContent)
		return
	}
	c.Redirect(http.StatusSeeOther, location)
}

func (h *AccessAdmin) boundaryFailure(c *gin.Context, err error) bool {
	if errors.Is(err, auth.ErrSession) {
		h.auth.sessionFailure(c, err)
		return true
	}
	if errors.Is(err, auth.ErrAuthorizationEpoch) {
		if c.GetHeader("HX-Request") == "true" {
			c.Header("HX-Redirect", "/account")
		}
		Render(c, http.StatusForbidden, views.AuthError(h.assets, "Your permissions changed. Refresh your account and try again."))
		return true
	}
	return false
}

func adminFeedback(message string, err error) (int, string, string) {
	if err == nil {
		return http.StatusOK, message, ""
	}
	status, problem := adminError(err)
	return status, "", problem
}

func adminSection(location string) (section, groupID string) {
	switch {
	case strings.Contains(location, "/users"):
		return "users", ""
	case strings.Contains(location, "/groups/"):
		groupID = strings.Split(strings.TrimPrefix(location, "/admin/groups/"), "/")[0]
		if strings.Contains(location, "/members") {
			return "members", groupID
		}
		return "databases", groupID
	case location == "/admin/groups":
		return "groups", ""
	default:
		return "dashboard", ""
	}
}
func adminError(err error) (int, string) {
	switch {
	case errors.Is(err, auth.ErrSession):
		return http.StatusUnauthorized, "Your session is no longer valid. Please sign in again."
	case errors.Is(err, auth.ErrInvalidInput), errors.Is(err, access.ErrInvalidInput), errors.Is(err, access.ErrInvalidPermission):
		return http.StatusUnprocessableEntity, "Check the submitted values and try again."
	case errors.Is(err, auth.ErrUsernameTaken), errors.Is(err, access.ErrConflict):
		return http.StatusConflict, "That username or name is already in use."
	case errors.Is(err, auth.ErrUserNotFound), errors.Is(err, access.ErrNotFound):
		return http.StatusNotFound, "The requested user, group, or target was not found."
	case errors.Is(err, auth.ErrBusy):
		return http.StatusServiceUnavailable, "The service is busy. Try again shortly."
	default:
		return http.StatusServiceUnavailable, "The change could not be completed. Try again shortly."
	}
}
