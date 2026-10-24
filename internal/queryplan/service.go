package queryplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/database"
)

type Authorizer interface {
	Require(ctx context.Context, userID, databaseID string, required access.Permission) error
}

type Service struct {
	authz    Authorizer
	resolver database.Resolver
}

func NewService(authz Authorizer, resolver database.Resolver) *Service {
	return &Service{authz: authz, resolver: resolver}
}

type Request struct {
	SQL       string
	Mode      Mode
	Confirmed bool
}

// Explain authorizes, validates and plans one statement. SELECT needs view
// access; DML and ANALYZE need edit access. Nothing is recorded in history.
func (s *Service) Explain(ctx context.Context, databaseID string, req Request) (Result, error) {
	userID, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return Result{}, auth.ErrSession
	}
	if err := s.authz.Require(ctx, userID, databaseID, access.View); err != nil {
		return Result{}, err
	}
	kind, err := Validate(req.SQL, req.Mode)
	if err != nil {
		return Result{}, err
	}
	if kind == Write || req.Mode == ModeAnalyze {
		if err := s.authz.Require(ctx, userID, databaseID, access.Edit); err != nil {
			return Result{}, err
		}
	}
	if req.Mode == ModeAnalyze && !req.Confirmed {
		return Result{}, &database.Error{Kind: database.ErrValidation, Message: "Confirm that ANALYZE executes the SELECT on the sandbox database."}
	}
	handle, err := s.resolver.Open(ctx, databaseID)
	if err != nil {
		return Result{}, err
	}
	defer handle.Close()
	explainer, err := database.As[database.Explainer](handle)
	if err != nil {
		return Result{}, &database.Error{Kind: database.ErrUnsupportedDialect, Message: "Query plans are not available for " + handle.DBMS().Label() + " connections."}
	}
	raw, err := explainer.Explain(ctx, req.SQL, req.Mode == ModeAnalyze)
	if err != nil {
		return Result{}, err
	}
	root, planning, execution, err := ParsePostgres(raw)
	if err != nil {
		return Result{}, errors.New("unreadable plan")
	}
	sum := sha256.Sum256([]byte(req.SQL))
	result := Result{Dialect: string(handle.DBMS()), Mode: req.Mode, SQLHash: hex.EncodeToString(sum[:]), Root: root, PlanningTime: planning, ExecutionTime: execution}
	if req.Mode == ModeAnalyze {
		result.Warnings = append(result.Warnings, "ANALYZE executed this SELECT in a read-only transaction that was rolled back.")
	} else {
		result.Warnings = append(result.Warnings, "Estimated plan only; the statement was not executed, so actual time and rows are not available.")
	}
	return result, nil
}
