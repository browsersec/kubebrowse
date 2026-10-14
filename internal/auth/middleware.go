package auth

import (
	"net/http"

	"github.com/browsersec/KubeBrowse/internal/httpx"
	"github.com/sirupsen/logrus"
)

// AuthMiddleware is a middleware that validates user sessions
func AuthMiddleware(service *Service) httpx.HandlerFunc {
	return func(c *httpx.Context) {
		// Get session token from cookie
		sessionToken, err := c.Cookie(SessionCookieName)
		if err != nil {
			logrus.Debugf("AuthMiddleware: No session cookie found: %v", err)
			c.JSON(http.StatusUnauthorized, httpx.H{"error": "Authentication required"})
			c.Abort()
			return
		}

		logrus.Debugf("AuthMiddleware: Found session token: %s", sessionToken)

		// Validate session
		logrus.Debugf("AuthMiddleware: Validating session token: %s", sessionToken)
		user, session, err := service.ValidateSession(sessionToken)
		if err != nil {
			if err == ErrSessionExpired {
				logrus.Debugf("AuthMiddleware: Session expired for token: %s", sessionToken)
				// Clear invalid cookie
				c.SetCookie(SessionCookieName, "", -1, "/", "", false, true)
				c.JSON(http.StatusUnauthorized, httpx.H{"error": "Session expired"})
				c.Abort()
				return
			}
			logrus.Errorf("Session validation error: %v", err)
			c.JSON(http.StatusInternalServerError, httpx.H{"error": "Session validation failed"})
			c.Abort()
			return
		}

		logrus.Debugf("AuthMiddleware: Session validated successfully for user: %s", user.Email)

		// Set user in context
		c.Set(UserContextKey, user)
		c.Set("session", session)

		c.Next()
	}
}

// OptionalAuthMiddleware is a middleware that optionally validates user sessions
// It doesn't abort the request if authentication fails, but sets user context if available
func OptionalAuthMiddleware(service *Service) httpx.HandlerFunc {
	return func(c *httpx.Context) {
		// Get session token from cookie
		sessionToken, err := c.Cookie(SessionCookieName)
		if err != nil {
			// No session cookie, continue without authentication
			c.Next()
			return
		}

		// Validate session
		user, session, err := service.ValidateSession(sessionToken)
		if err != nil {
			if err == ErrSessionExpired {
				// Clear invalid cookie
				c.SetCookie(SessionCookieName, "", -1, "/", "", false, true)
			}
			// Continue without authentication
			c.Next()
			return
		}

		// Set user in context
		c.Set(UserContextKey, user)
		c.Set("session", session)

		c.Next()
	}
}
