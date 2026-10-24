package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/database"
	"sqlwise/internal/queryplan"
	"sqlwise/internal/web/views"
)

type Explain struct {
	auth   *Auth
	policy *access.Service
	plans  *queryplan.Service
	assets views.Assets
}

func NewExplain(authHandler *Auth, policy *access.Service, plans *queryplan.Service, assets views.Assets) *Explain {
	return &Explain{auth: authHandler, policy: policy, plans: plans, assets: assets}
}

func (h *Explain) Register(router *gin.Engine) {
	router.POST("/databases/:databaseID/explain", h.auth.RequireSession(),
		requireDatabasePermissionWithFailure(h.auth.service.ValidateContextFresh, h.policy, "databaseID", access.View, h.fail), h.explain)
}

func (h *Explain) explain(c *gin.Context) {
	sql := c.PostForm("sql")
	if sql == "" {
		sql = c.PostForm("stored_sql")
	}
	result, err := h.plans.Explain(c.Request.Context(), c.Param("databaseID"), queryplan.Request{SQL: sql, Mode: queryplan.Mode(c.PostForm("mode")), Confirmed: c.PostForm("confirm") == "on"})
	if err != nil {
		h.fail(c, err)
		return
	}
	plan := planView(result)
	if c.GetHeader("HX-Request") == "true" {
		Render(c, http.StatusOK, views.PlanResult(plan))
		return
	}
	session := c.MustGet("auth_session").(auth.Session)
	Render(c, http.StatusOK, views.PlanPage(h.assets, session.Principal.Username, plan))
}

func (h *Explain) fail(c *gin.Context, err error) {
	c.Abort()
	if errors.Is(err, auth.ErrSession) {
		h.auth.rejectSession(c)
		return
	}
	status, message := http.StatusInternalServerError, "The plan could not be produced. Please try again."
	var domain *database.Error
	switch {
	case errors.Is(err, auth.ErrAuthorizationEpoch):
		status, message = http.StatusForbidden, "Your access changed. Reload the page to continue."
	case errors.Is(err, access.ErrPermissionDenied):
		status, message = http.StatusForbidden, "You need edit access to plan writes or run Analyze on this database."
	case errors.As(err, &domain):
		status, message = connectionError(err)
	}
	if c.GetHeader("HX-Request") == "true" {
		Render(c, status, views.PlanError(message))
		return
	}
	Render(c, status, views.DatabaseError(h.assets, message))
}

func planView(r queryplan.Result) views.PlanView {
	return views.PlanView{Dialect: r.Dialect, Mode: string(r.Mode), SQLHash: r.SQLHash, Warnings: r.Warnings,
		PlanningTime: milliseconds(r.PlanningTime), ExecutionTime: milliseconds(r.ExecutionTime), Root: planNodeView(r.Root)}
}

func planNodeView(n queryplan.Node) views.PlanNodeView {
	var detail []string
	switch {
	case n.Operation != "" && n.Relation != "":
		detail = append(detail, n.Operation+" on "+n.Relation)
	case n.Relation != "":
		detail = append(detail, "on "+n.Relation)
	}
	if n.Index != "" {
		detail = append(detail, "using "+n.Index)
	}
	if n.Join != "" {
		detail = append(detail, n.Join+" join")
	}
	v := views.PlanNodeView{Type: n.Type, Detail: strings.Join(detail, " · "), Cost: number(n.StartupCost) + "–" + number(n.TotalCost), Rows: number(n.PlanRows),
		ActualTime: optional(n.ActualTime), ActualRows: optional(n.ActualRows), Loops: optional(n.Loops)}
	for _, child := range n.Children {
		v.Children = append(v.Children, planNodeView(child))
	}
	return v
}

func number(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func optional(v *float64) string {
	if v == nil {
		return "N/A"
	}
	return number(*v)
}

func milliseconds(v *float64) string {
	if v == nil {
		return "N/A"
	}
	return number(*v) + " ms"
}
