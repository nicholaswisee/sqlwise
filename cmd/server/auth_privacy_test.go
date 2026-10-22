package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAuthErrorsAndLogsDoNotExposeSecrets(t *testing.T) {
	var logs bytes.Buffer
	previousGin, previousLog := gin.DefaultWriter, log.Writer()
	gin.DefaultWriter = &logs
	log.SetOutput(&logs)
	t.Cleanup(func() { gin.DefaultWriter = previousGin; log.SetOutput(previousLog) })
	f := newAuthFixture(t)
	input := f.loginRequest(t)
	token := input.cookies[0].Value
	input.path = "/login?password=private-query-value"
	response := f.do(input)
	if response.Code != 400 {
		t.Fatal("query-string credentials accepted")
	}
	input.path = "/login"
	f.pool.Close()
	response = f.do(input)
	if response.Code != 503 {
		t.Fatal("database error not handled")
	}
	for _, secret := range []string{token, input.form.Get("password"), "private-query-value", "postgres://", "password_hash"} {
		if strings.Contains(logs.String(), secret) || strings.Contains(response.Body.String(), secret) {
			t.Fatal("secret exposed in logs or response")
		}
	}
}
