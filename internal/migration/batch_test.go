package migration

import (
	"errors"
	"strings"
	"testing"
)

func TestBatchTemplateRendersTheReviewedStatement(t *testing.T) {
	spec := BatchSpec{Schema: "public", Table: "accounts", Key: "id", Column: "status", Value: "it's archived"}
	if got := spec.render(); got != `UPDATE "public"."accounts" SET "status" = 'it''s archived';` {
		t.Fatalf("render = %q", got)
	}
	in, err := DraftInput{Title: "Archive", SQL: "DROP TABLE accounts", Batch: &spec, BatchSize: 500}.normalize()
	if err != nil || in.SQL != spec.render() {
		t.Fatalf("normalize = %q, %v; batch SQL must come from the template", in.SQL, err)
	}
	if _, err := ParsePostgres(in.SQL); err != nil {
		t.Fatalf("rendered SQL does not validate: %v", err)
	}
}

func TestBatchTemplateValidation(t *testing.T) {
	ok := BatchSpec{Schema: "public", Table: "accounts", Key: "id", Column: "status"}
	cases := map[string]struct {
		spec BatchSpec
		size int
	}{
		"injected table":  {BatchSpec{Schema: "public", Table: "a; DROP TABLE a", Key: "id", Column: "c"}, 10},
		"receipt schema":  {BatchSpec{Schema: "sqlwise_internal", Table: "t", Key: "id", Column: "c"}, 10},
		"key is column":   {BatchSpec{Schema: "public", Table: "t", Key: "id", Column: "id"}, 10},
		"zero size":       {ok, 0},
		"oversized":       {ok, MaxBatchSize + 1},
		"nul in value":    {BatchSpec{Schema: "public", Table: "t", Key: "id", Column: "c", Value: "a\x00"}, 10},
		"oversized value": {BatchSpec{Schema: "public", Table: "t", Key: "id", Column: "c", Value: strings.Repeat("x", 5000)}, 10},
	}
	for name, c := range cases {
		if err := c.spec.validate(c.size); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := ok.validate(1000); err != nil {
		t.Fatal(err)
	}
}

func TestDefinitionHashBindsTheBatchTemplate(t *testing.T) {
	plain := Draft{SQL: "UPDATE t SET c = 'x';", SQLDown: "UPDATE t SET c = 'y';"}
	before := plain.DefinitionHash()
	batch := plain
	batch.Batch, batch.BatchSize = &BatchSpec{Schema: "public", Table: "t", Key: "id", Column: "c", Value: "x"}, 100
	if batch.DefinitionHash() == before {
		t.Fatal("adding a batch template kept the reviewed hash")
	}
	resized := batch
	resized.BatchSize = 200
	if resized.DefinitionHash() == batch.DefinitionHash() {
		t.Fatal("changing the batch size kept the reviewed hash")
	}
	if plain.DefinitionHash() != before {
		t.Fatal("non-batch hashes must stay stable for existing approvals")
	}
}
