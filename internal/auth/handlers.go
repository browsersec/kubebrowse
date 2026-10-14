package auth

import (
	"net/http"
	"os"
	"strings"

	"github.com/browsersec/KubeBrowse/internal/httpx"
	"github.com/sirupsen/logrus"
)

const (
	SessionCookieName = "kubebrowse_session"
	UserContextKey    = "user"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
	}
}

// RegisterRequest represents the request body for email registration
type RegisterRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
}

// LoginRequest represents the request body for email login
type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// AuthResponse represents the response for successful authentication
type AuthResponse struct {
	User    *User  `json:"user"`
	Message string `json:"message"`
}

// RegisterWithEmail handles user registration with email and password
func (h *Handler) RegisterWithEmail(c *httpx.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, httpx.H{"error": err.Error()})
		return
	}

	user, err := h.service.RegisterWithEmail(req.Email, req.Password)
	if err != nil {
		if err == ErrUserAlreadyExists {
			c.JSON(http.StatusConflict, httpx.H{"error": "User already exists"})
			return
		}
		logrus.Errorf("Failed to register user: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to register user"})
		return
	}

	// Don't create session immediately - user needs to verify email first
	c.JSON(http.StatusCreated, httpx.H{
		"user":    user,
		"message": "User registered successfully. Please check your email to verify your account.",
	})
}

// LoginWithEmail handles user login with email and password
func (h *Handler) LoginWithEmail(c *httpx.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, httpx.H{"error": err.Error()})
		return
	}

	user, session, err := h.service.LoginWithEmail(req.Email, req.Password)
	if err != nil {
		if err == ErrInvalidCredentials {
			c.JSON(http.StatusUnauthorized, httpx.H{"error": "Invalid credentials"})
			return
		}
		if err == ErrEmailNotVerified {
			c.JSON(http.StatusForbidden, httpx.H{
				"error": "Email not verified. Please check your email and verify your account.",
				"code":  "EMAIL_NOT_VERIFIED",
			})
			return
		}
		logrus.Errorf("Failed to login user: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to login"})
		return
	}

	// Set session cookie
	h.setSessionCookie(c, session.SessionToken)

	c.JSON(http.StatusOK, AuthResponse{
		User:    user,
		Message: "Login successful",
	})
}

// Logout handles user logout
func (h *Handler) Logout(c *httpx.Context) {
	// Get session token from cookie
	sessionToken, err := c.Cookie(SessionCookieName)
	if err == nil && sessionToken != "" {
		// Delete session from database
		err = h.service.DeleteSession(sessionToken)
		if err != nil {
			logrus.Errorf("Failed to delete session: %v", err)
		}
	}

	// Clear session cookie
	h.clearSessionCookie(c)

	c.JSON(http.StatusOK, httpx.H{"message": "Logged out successfully"})
}

// GetCurrentUser returns the current authenticated user
func (h *Handler) GetCurrentUser(c *httpx.Context) {
	user, exists := c.Get(UserContextKey)
	if !exists {
		c.JSON(http.StatusUnauthorized, httpx.H{"error": "Not authenticated"})
		return
	}

	c.JSON(http.StatusOK, httpx.H{"user": user})
}

// UpdateProfileRequest represents the request body for profile updates
type UpdateProfileRequest struct {
	Username  *string `json:"username"`
	Name      *string `json:"name"`
	AvatarURL *string `json:"avatar_url"`
}

// UpdatePasswordRequest represents the request body for password updates
type UpdatePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required,min=8"`
}

// UpdateProfile handles user profile updates
func (h *Handler) UpdateProfile(c *httpx.Context) {
	user, exists := c.Get(UserContextKey)
	if !exists {
		c.JSON(http.StatusUnauthorized, httpx.H{"error": "Not authenticated"})
		return
	}

	authUser := user.(*User)

	var req UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, httpx.H{"error": err.Error()})
		return
	}

	// Update user profile
	updatedUser, err := h.service.UpdateUserProfile(authUser.ID, req.Username, req.Name, req.AvatarURL)
	if err != nil {
		logrus.Errorf("Failed to update user profile: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to update profile"})
		return
	}

	c.JSON(http.StatusOK, httpx.H{
		"user":    updatedUser,
		"message": "Profile updated successfully",
	})
}

// UpdatePassword handles user password updates
func (h *Handler) UpdatePassword(c *httpx.Context) {
	user, exists := c.Get(UserContextKey)
	if !exists {
		c.JSON(http.StatusUnauthorized, httpx.H{"error": "Not authenticated"})
		return
	}

	authUser := user.(*User)

	var req UpdatePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, httpx.H{"error": err.Error()})
		return
	}

	// Verify current password
	_, session, err := h.service.LoginWithEmail(authUser.Email, req.CurrentPassword)
	if err != nil {
		c.JSON(http.StatusBadRequest, httpx.H{"error": "Current password is incorrect"})
		return
	}

	// Update password
	err = h.service.UpdateUserPassword(authUser.ID, req.NewPassword)
	if err != nil {
		logrus.Errorf("Failed to update password: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to update password"})
		return
	}

	// Delete old session and create new one
	if session != nil {
		if err := h.service.DeleteSession(session.SessionToken); err != nil {
			logrus.Errorf("Failed to delete old session: %v", err)
		}
	}

	newSession, err := h.service.CreateSession(authUser.ID)
	if err != nil {
		logrus.Errorf("Failed to create new session: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to create new session"})
		return
	}

	// Set new session cookie
	h.setSessionCookie(c, newSession.SessionToken)

	c.JSON(http.StatusOK, httpx.H{
		"message": "Password updated successfully",
	})
}

// GetUserProfile returns the current user's profile
func (h *Handler) GetUserProfile(c *httpx.Context) {
	user, exists := c.Get(UserContextKey)
	if !exists {
		c.JSON(http.StatusUnauthorized, httpx.H{"error": "Not authenticated"})
		return
	}

	authUser := user.(*User)

	// Get fresh user data from database
	freshUser, err := h.service.GetUserByID(authUser.ID)
	if err != nil {
		logrus.Errorf("Failed to get user profile: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to get user profile"})
		return
	}

	c.JSON(http.StatusOK, httpx.H{"user": freshUser})
}

// setSessionCookie sets the session cookie
func (h *Handler) setSessionCookie(c *httpx.Context, sessionToken string) {
	// Get the request host to set proper cookie domain
	host := c.Request.Host
	var domain string
	var secure bool

	// For local development with proxy, set cookie for localhost
	if strings.Contains(host, "localhost") || strings.Contains(host, "127.0.0.1") {
		domain = "localhost"
		secure = false // HTTP for local development
	} else {
		// For production, extract domain from host
		if strings.Contains(host, ":") {
			domain = strings.Split(host, ":")[0]
		} else {
			domain = host
		}
		secure = true // HTTPS for production
	}

	logrus.Debugf("Setting session cookie for host: %s, domain: %s, secure: %v", host, domain, secure)

	c.SetCookie(
		SessionCookieName,
		sessionToken,
		7*24*60*60, // 7 days
		"/",        // Path - root path to ensure it's sent with all requests
		domain,     // Domain - set to localhost for local development
		secure,     // Secure - false for local development, true for production
		true,       // httpOnly
	)
}

// VerifyEmailRequest represents the request body for email verification
type VerifyEmailRequest struct {
	Token string `json:"token" binding:"required"`
}

// ResendVerificationRequest represents the request body for resending verification email
type ResendVerificationRequest struct {
	Email string `json:"email" binding:"required,email"`
}

// VerifyEmail handles email verification
func (h *Handler) VerifyEmail(c *httpx.Context) {
	token := c.Query("token")
	if token == "" {
		// Try to get token from request body
		var req VerifyEmailRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, httpx.H{"error": "Verification token is required"})
			return
		}
		token = req.Token
	}

	user, err := h.service.VerifyEmail(token)
	if err != nil {
		if err == ErrInvalidToken {
			c.JSON(http.StatusBadRequest, httpx.H{"error": "Invalid or expired verification token"})
			return
		}
		logrus.Errorf("Failed to verify email: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to verify email"})
		return
	}

	// Create session for the verified user
	session, err := h.service.CreateSession(user.ID)
	if err != nil {
		logrus.Errorf("Failed to create session after verification: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to create session"})
		return
	}

	// Set session cookie
	h.setSessionCookie(c, session.SessionToken)

	// If this is a GET request (from email link), redirect to frontend
	if c.Request.Method == "GET" {
		frontendURL := os.Getenv("FRONTEND_URL")
		if frontendURL == "" {
			frontendURL = "http://localhost:5173"
		}
		c.Redirect(http.StatusTemporaryRedirect, frontendURL+"/auth/verification-success")
		return
	}

	// For API requests, return JSON response
	c.JSON(http.StatusOK, httpx.H{
		"user":    user,
		"message": "Email verified successfully",
	})
}

// ResendVerificationEmail handles resending verification email
func (h *Handler) ResendVerificationEmail(c *httpx.Context) {
	var req ResendVerificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, httpx.H{"error": err.Error()})
		return
	}

	err := h.service.ResendVerificationEmail(req.Email)
	if err != nil {
		if err == ErrUserNotFound {
			c.JSON(http.StatusNotFound, httpx.H{"error": "User not found or email already verified"})
			return
		}
		logrus.Errorf("Failed to resend verification email: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to resend verification email"})
		return
	}

	c.JSON(http.StatusOK, httpx.H{
		"message": "Verification email sent successfully",
	})
}

// clearSessionCookie clears the session cookie
func (h *Handler) clearSessionCookie(c *httpx.Context) {
	c.SetCookie(
		SessionCookieName,
		"",
		-1,
		"/",
		"",
		false,
		true,
	)
}
