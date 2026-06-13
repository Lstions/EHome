package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// envelope is the unified API response structure.
// All API endpoints should return this format so the frontend can
// consistently unpack with a single `response.data` access.
type envelope struct {
	Code    int         `json:"code"`
	Data    interface{} `json:"data"`
	Message string      `json:"message"`
}

// Success sends a 200 response with the standard envelope.
//   Success(c, items) → {"code":200, "data":items, "message":"ok"}
func Success(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, envelope{
		Code:    http.StatusOK,
		Data:    data,
		Message: "ok",
	})
}

// SuccessMsg sends a 200 response with a custom message.
//   SuccessMsg(c, result, "created") → {"code":200, "data":result, "message":"created"}
func SuccessMsg(c *gin.Context, data interface{}, msg string) {
	c.JSON(http.StatusOK, envelope{
		Code:    http.StatusOK,
		Data:    data,
		Message: msg,
	})
}

// SuccessWithCode sends a response with a custom HTTP status code.
//   SuccessWithCode(c, http.StatusCreated, item) → 201 {"code":201, "data":item, "message":"ok"}
func SuccessWithCode(c *gin.Context, statusCode int, data interface{}) {
	c.JSON(statusCode, envelope{
		Code:    statusCode,
		Data:    data,
		Message: "ok",
	})
}

// SuccessWithCodeMsg sends a response with a custom HTTP status code and message.
//   SuccessWithCodeMsg(c, http.StatusCreated, item, "created") → 201 {"code":201, "data":item, "message":"created"}
func SuccessWithCodeMsg(c *gin.Context, statusCode int, data interface{}, msg string) {
	c.JSON(statusCode, envelope{
		Code:    statusCode,
		Data:    data,
		Message: msg,
	})
}

// Error sends an error response with the standard envelope (data=null).
//   Error(c, http.StatusNotFound, "not found") → 404 {"code":404, "data":null, "message":"not found"}
func Error(c *gin.Context, statusCode int, msg string) {
	c.JSON(statusCode, envelope{
		Code:    statusCode,
		Data:    nil,
		Message: msg,
	})
}
