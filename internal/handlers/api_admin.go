package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
)

type apiAdminUser struct {
	ID              string `json:"id"`
	Username        string `json:"username"`
	Active          bool   `json:"active"`
	IsPlatformAdmin bool   `json:"is_platform_admin"`
}
type apiAdminGroup struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Active       bool   `json:"active"`
	LeaderUserID string `json:"leader_user_id"`
}
type apiAdminDatabase struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}
type apiAdminMembership struct {
	UserID    string `json:"user_id"`
	GroupID   string `json:"group_id"`
	Username  string `json:"username"`
	GroupName string `json:"group_name"`
}
type apiAdminGrant struct {
	GroupID      string `json:"group_id"`
	DatabaseID   string `json:"database_id"`
	GroupName    string `json:"group_name"`
	DatabaseName string `json:"database_name"`
	Permission   string `json:"permission"`
}
type adminSnapshotDTO struct {
	Users         []apiAdminUser       `json:"users"`
	Groups        []apiAdminGroup      `json:"groups"`
	Databases     []apiAdminDatabase   `json:"databases"`
	Memberships   []apiAdminMembership `json:"memberships"`
	Grants        []apiAdminGrant      `json:"grants"`
	SelectedGroup *apiAdminGroup       `json:"selected_group,omitempty"`
}

func (h *AccessAdmin) RegisterAPI(router *gin.Engine) {
	a := router.Group("/api/v1/admin", h.auth.RequireAPISession(), h.auth.RequireAPIPlatformAdmin())
	a.GET("/users", h.apiUsers)
	a.POST("/users", h.apiCreateUser)
	a.POST("/users/:id", h.apiUpdateUser)
	a.POST("/users/:id/disable", h.apiDisableUser)
	a.POST("/users/:id/active", h.apiUserActive)
	a.POST("/users/:id/admin", h.apiPlatformAdmin)
	a.GET("/groups", h.apiGroups)
	a.POST("/groups", h.apiCreateGroup)
	a.POST("/groups/:id", h.apiUpdateGroup)
	a.POST("/groups/:id/disable", h.apiDisableGroup)
	a.POST("/groups/:id/active", h.apiGroupActive)
	a.POST("/groups/:id/leader", h.apiSetGroupLeader)
	a.GET("/groups/:id/members", h.apiMembers)
	a.POST("/groups/:id/members", h.apiAddMember)
	a.POST("/groups/:id/members/remove", h.apiRemoveMember)
	a.GET("/groups/:id/databases", h.apiGrants)
	a.POST("/groups/:id/databases", h.apiSetGrant)
	a.POST("/groups/:id/databases/remove", h.apiRemoveGrant)
}

func (h *AccessAdmin) apiSnapshot(c *gin.Context, groupID string) {
	snapshot, err := h.policy.AdminSnapshot(c.Request.Context())
	if err != nil {
		apiFailure(c, err)
		return
	}
	session, _ := c.Get("auth_session")
	data, err := accessPageData(snapshot, session.(auth.Session), groupID, "", "")
	if err != nil {
		apiFailure(c, err)
		return
	}
	dto := adminSnapshotDTO{
		Users: []apiAdminUser{}, Groups: []apiAdminGroup{}, Databases: []apiAdminDatabase{},
		Memberships: []apiAdminMembership{}, Grants: []apiAdminGrant{},
	}
	for _, x := range data.Users {
		dto.Users = append(dto.Users, apiAdminUser{x.ID, x.Username, x.Active, x.IsPlatformAdmin})
	}
	for _, x := range data.Groups {
		dto.Groups = append(dto.Groups, apiAdminGroup{x.ID, x.Name, x.Active, x.LeaderUserID})
	}
	for _, x := range data.Databases {
		dto.Databases = append(dto.Databases, apiAdminDatabase{x.ID, x.Name, x.Active})
	}
	for _, x := range data.Memberships {
		dto.Memberships = append(dto.Memberships, apiAdminMembership{x.UserID, x.GroupID, x.Username, x.GroupName})
	}
	for _, x := range data.Grants {
		dto.Grants = append(dto.Grants, apiAdminGrant{x.GroupID, x.DatabaseID, x.GroupName, x.DatabaseName, x.Permission})
	}
	if data.SelectedGroup.ID != "" {
		dto.SelectedGroup = &apiAdminGroup{data.SelectedGroup.ID, data.SelectedGroup.Name, data.SelectedGroup.Active, data.SelectedGroup.LeaderUserID}
	}
	apiData(c, dto)
}
func (h *AccessAdmin) apiUsers(c *gin.Context)   { h.apiSnapshot(c, "") }
func (h *AccessAdmin) apiGroups(c *gin.Context)  { h.apiSnapshot(c, "") }
func (h *AccessAdmin) apiMembers(c *gin.Context) { h.apiSnapshot(c, c.Param("id")) }
func (h *AccessAdmin) apiGrants(c *gin.Context)  { h.apiSnapshot(c, c.Param("id")) }
func (h *AccessAdmin) apiMutate(c *gin.Context, groupID string, err error) {
	if err != nil {
		apiAdminFailure(c, err)
		return
	}
	if _, err = h.auth.service.ValidateContextFresh(c.Request.Context()); err != nil {
		apiFailure(c, err)
		return
	}
	h.apiSnapshot(c, groupID)
}

func apiAdminFailure(c *gin.Context, err error) {
	status, _ := adminError(err)
	code, message := "unavailable", "The change could not be completed. Try again shortly."
	switch status {
	case http.StatusUnprocessableEntity:
		code, message = "validation", "Check the submitted values and try again."
	case http.StatusConflict:
		code, message = "conflict", "The requested value is already in use."
	case http.StatusNotFound:
		code, message = "not_found", "The requested user, group, or target was not found."
	case http.StatusUnauthorized:
		code, message = "session_expired", "Your session has expired."
	case http.StatusServiceUnavailable:
		code, message = "unavailable", "The service is temporarily unavailable."
	}
	if errors.Is(err, auth.ErrAuthorizationEpoch) {
		status, code, message = http.StatusConflict, "authorization_stale", "Your permissions changed. Refresh and try again."
	}
	apiError(c, status, code, message)
}
func (h *AccessAdmin) apiCreateUser(c *gin.Context) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !apiDecode(c, &in) {
		return
	}
	creds, err := auth.ParseCredentials(in.Username, in.Password)
	if err == nil {
		_, err = h.auth.service.CreateUser(c.Request.Context(), creds)
	}
	h.apiMutate(c, "", err)
}
func (h *AccessAdmin) apiUpdateUser(c *gin.Context) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !apiDecode(c, &in) {
		return
	}
	name, password, err := userUpdateInput(in.Username, in.Password)
	if err == nil {
		if name != "" {
			err = h.auth.service.RenameUser(c.Request.Context(), c.Param("id"), name)
		} else {
			err = h.auth.service.UpdateUserPassword(c.Request.Context(), c.Param("id"), password)
		}
	}
	h.apiMutate(c, "", err)
}
func (h *AccessAdmin) apiDisableUser(c *gin.Context) {
	h.apiMutate(c, "", h.auth.service.SetUserActive(c.Request.Context(), c.Param("id"), false))
}
func (h *AccessAdmin) apiUserActive(c *gin.Context) {
	var in struct {
		Active *bool `json:"active"`
	}
	if !apiDecode(c, &in) {
		return
	}
	if in.Active == nil {
		apiError(c, 422, "invalid_input", "Active must be true or false.")
		return
	}
	h.apiMutate(c, "", h.auth.service.SetUserActive(c.Request.Context(), c.Param("id"), *in.Active))
}
func (h *AccessAdmin) apiPlatformAdmin(c *gin.Context) {
	var in struct {
		Admin *bool `json:"admin"`
	}
	if !apiDecode(c, &in) {
		return
	}
	if in.Admin == nil {
		apiError(c, 422, "invalid_input", "Admin must be true or false.")
		return
	}
	h.apiMutate(c, "", h.auth.service.SetPlatformAdmin(c.Request.Context(), c.Param("id"), *in.Admin))
}
func (h *AccessAdmin) apiCreateGroup(c *gin.Context) {
	var in struct {
		Name string `json:"name"`
	}
	if !apiDecode(c, &in) {
		return
	}
	_, err := h.policy.CreateGroup(c.Request.Context(), in.Name)
	h.apiMutate(c, "", err)
}
func (h *AccessAdmin) apiUpdateGroup(c *gin.Context) {
	var in struct {
		Name string `json:"name"`
	}
	if !apiDecode(c, &in) {
		return
	}
	h.apiMutate(c, "", h.policy.RenameGroup(c.Request.Context(), c.Param("id"), in.Name))
}
func (h *AccessAdmin) apiDisableGroup(c *gin.Context) {
	h.apiMutate(c, "", h.policy.SetGroupActive(c.Request.Context(), c.Param("id"), false))
}
func (h *AccessAdmin) apiGroupActive(c *gin.Context) {
	var in struct {
		Active *bool `json:"active"`
	}
	if !apiDecode(c, &in) {
		return
	}
	if in.Active == nil {
		apiError(c, 422, "invalid_input", "Active must be true or false.")
		return
	}
	h.apiMutate(c, "", h.policy.SetGroupActive(c.Request.Context(), c.Param("id"), *in.Active))
}
func (h *AccessAdmin) apiSetGroupLeader(c *gin.Context) {
	var in struct {
		UserID *string `json:"user_id"`
	}
	if !apiDecode(c, &in) {
		return
	}
	if in.UserID == nil {
		apiError(c, 422, "invalid_input", "User ID is required; use an empty value to clear the leader.")
		return
	}
	h.apiMutate(c, c.Param("id"), h.policy.SetGroupLeader(c.Request.Context(), c.Param("id"), *in.UserID))
}
func (h *AccessAdmin) apiAddMember(c *gin.Context) {
	var in struct {
		UserID string `json:"user_id"`
	}
	if !apiDecode(c, &in) {
		return
	}
	h.apiMutate(c, c.Param("id"), h.policy.AddMembership(c.Request.Context(), in.UserID, c.Param("id")))
}
func (h *AccessAdmin) apiRemoveMember(c *gin.Context) {
	var in struct {
		UserID string `json:"user_id"`
	}
	if !apiDecode(c, &in) {
		return
	}
	h.apiMutate(c, c.Param("id"), h.policy.RemoveMembership(c.Request.Context(), in.UserID, c.Param("id")))
}
func (h *AccessAdmin) apiSetGrant(c *gin.Context) {
	var in struct {
		DatabaseID string `json:"database_id"`
		Permission string `json:"permission"`
	}
	if !apiDecode(c, &in) {
		return
	}
	p, err := access.ParsePermission(in.Permission)
	if err == nil {
		err = h.policy.SetGrant(c.Request.Context(), c.Param("id"), in.DatabaseID, p)
	}
	h.apiMutate(c, c.Param("id"), err)
}
func (h *AccessAdmin) apiRemoveGrant(c *gin.Context) {
	var in struct {
		DatabaseID string `json:"database_id"`
	}
	if !apiDecode(c, &in) {
		return
	}
	h.apiMutate(c, c.Param("id"), h.policy.RemoveGrant(c.Request.Context(), c.Param("id"), in.DatabaseID))
}
