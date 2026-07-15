package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const multiUserRemovedCode = "MULTI_USER_API_REMOVED"

func registerLegacyUserRoutes(v1 *gin.RouterGroup) {
	gone := func(c *gin.Context) {
		c.Header("Deprecation", "true")
		c.Header("Sunset", time.Now().UTC().AddDate(0, 3, 0).Format(http.TimeFormat))
		c.Header("Link", `</api/v1/account>; rel="successor-version"`)
		c.JSON(http.StatusGone, gin.H{
			"code":    multiUserRemovedCode,
			"message": "multi-user API has been removed; use /api/v1/account",
		})
	}

	u := v1.Group("/users")
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		u.Handle(method, "", gone)
		u.Handle(method, "/:id", gone)
		u.Handle(method, "/:id/reset-password", gone)
	}
	// Compatibility password endpoint delegates at the routing layer in a later
	// cleanup phase; until then it is explicitly retired rather than mutating an
	// arbitrary user.
	u.POST("/me/change-password", gone)
}
