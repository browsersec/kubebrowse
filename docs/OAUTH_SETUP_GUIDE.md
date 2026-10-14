# OAuth Setup Guide for KubeBrowse

This guide explains how to set up and troubleshoot the OAuth authentication flow between the frontend and backend in your KubeBrowse application.

## Overview

The OAuth flow works as follows:
1. User clicks "Continue with GitHub" on the frontend
2. Frontend redirects to `/auth/oauth/github` (proxied to backend via Caddy)
3. Backend redirects to GitHub OAuth
4. User authenticates with GitHub
5. GitHub redirects back to backend callback
6. Backend creates session and sets cookie
7. Backend redirects to frontend `/auth/success`
8. Frontend detects OAuth success and establishes session
9. User is redirected to dashboard

## Prerequisites

- Kind Kubernetes cluster running
- Tilt development environment
- GitHub OAuth app configured
- PostgreSQL and Redis running

## Configuration

### 1. Environment Variables

Ensure these environment variables are set in your backend:

```bash
# GitHub OAuth
GITHUB_CLIENT_ID=your_github_client_id
GITHUB_CLIENT_SECRET=your_github_client_secret
GITHUB_CALLBACK_URL=http://localhost:4000/auth/oauth/github/callback

# Frontend URL
FRONTEND_URL=http://localhost:3000

# Session
SESSION_SECRET=your_long_random_secret
```

### 2. GitHub OAuth App Setup

1. Go to GitHub Settings > Developer settings > OAuth Apps
2. Create a new OAuth App
3. Set Authorization callback URL to: `http://localhost:4000/auth/oauth/github/callback`
4. Copy Client ID and Client Secret

### 3. Frontend Proxy Configuration

The frontend uses **Caddy** as a reverse proxy to forward API requests:

```caddyfile
# Caddyfile
handle /auth/* {
    reverse_proxy {$CADDY_GUAC_CLIENT_URL} {
        transport http {
            tls
            tls_insecure_skip_verify
        }
        header_up Host {http.reverse_proxy.upstream.hostport}
        
        # Cookie handling for session management
        header_down Set-Cookie "Domain=localhost"
        header_down Set-Cookie "Path=/"
    }
}
```

**Environment Variable:**
- `CADDY_GUAC_CLIENT_URL=https://browser-sandbox-api.browser-sandbox.svc.cluster.local:8080`

## Troubleshooting

### Common Issues

#### 1. Cookies Not Being Set

**Symptoms:**
- User completes OAuth but session is not established
- `/auth/me` returns 401 after OAuth
- No session cookie in browser

**Solutions:**
- Check cookie domain settings in backend
- Ensure `SameSite=None` is not set for localhost
- Verify Caddy is forwarding cookies correctly
- Check `CADDY_GUAC_CLIENT_URL` environment variable

#### 2. CORS Issues

**Symptoms:**
- OAuth redirect fails
- Browser console shows CORS errors

**Solutions:**
- Backend should not set CORS for OAuth endpoints
- Caddy proxy handles CORS automatically

#### 3. Session Validation Fails

**Symptoms:**
- User appears authenticated but requests fail
- Session token mismatch

**Solutions:**
- Check Redis connection
- Verify session cleanup is working
- Check session token format

### Debugging Steps

#### 1. Check Backend Logs

```bash
kubectl logs -l app=browser-sandbox-api -n browser-sandbox -f
```

Look for:
- OAuth flow logs
- Session creation logs
- Cookie setting logs

#### 2. Check Frontend Logs

```bash
kubectl logs -l app=browser-sandbox-frontend -n browser-sandbox -f
```

Look for:
- Caddy proxy errors
- Authentication attempts

#### 3. Check Caddy Configuration

Verify the `CADDY_GUAC_CLIENT_URL` environment variable is set:

```bash
kubectl get deployment browser-sandbox-frontend -n browser-sandbox -o yaml | grep -A 10 env:
```

#### 4. Check Browser Network Tab

1. Open Developer Tools > Network
2. Complete OAuth flow
3. Check:
   - OAuth redirect requests
   - Cookie headers
   - Response status codes

#### 5. Test API Endpoints

```bash
# Test OAuth initiation
curl -v "http://localhost:4000/auth/oauth/github"

# Test session endpoint (should return 401)
curl -v "http://localhost:4000/auth/me"

# Test with session cookie
curl -v -H "Cookie: session=your_session_token" "http://localhost:4000/auth/me"
```

### Cookie Debugging

#### Check Cookie Settings

```bash
# In browser console
document.cookie

# Check specific cookie
document.cookie.split(';').find(c => c.trim().startsWith('session='))
```

#### Cookie Attributes

Ensure cookies have:
- `Domain=localhost` (for local development)
- `Path=/`
- `HttpOnly=true`
- `Secure=false` (for local development)

## Testing

### 1. Manual Testing

1. Start your development environment:
   ```bash
   tilt up
   ```

2. Open frontend in browser: `http://localhost:3000`

3. Click "Sign In" > "Continue with GitHub"

4. Complete GitHub OAuth

5. Verify you're redirected to `/auth/success`

6. Check if session is established

### 2. Automated Testing

Use the provided test script:

```bash
./test-oauth-flow.sh
```

This script will:
- Check cluster status
- Verify services are running
- Test OAuth endpoints
- Validate session management

## Production Considerations

### 1. HTTPS

- Set `Secure=true` for cookies
- Use proper SSL certificates
- Update OAuth callback URLs

### 2. Domain Configuration

- Update cookie domains for production
- Configure proper CORS policies
- Set appropriate SameSite cookie attributes

### 3. Session Management

- Implement session rotation
- Add rate limiting
- Monitor session usage

## Architecture Notes

### Frontend (React + Caddy)

- Uses React Context for auth state
- **Caddy reverse proxy** for API communication
- Handles OAuth callback routing

### Backend (Go + Gin)

- Goth library for OAuth providers
- Redis for session storage
- Cookie-based session management

### Infrastructure

- Kind cluster for local development
- Tilt for development workflow
- **Caddy** for frontend serving and proxying (instead of nginx)

## Support

If you continue to have issues:

1. Check the troubleshooting section above
2. Review backend and frontend logs
3. Verify environment configuration
4. Test with the provided test script
5. Check GitHub OAuth app settings
6. Verify Caddy configuration and environment variables

## References

- [Tilt Documentation](https://tilt.dev)
- [Goth OAuth Library](https://github.com/markbates/goth)
- [Caddy Reverse Proxy](https://caddy.community/t/reverse-proxy-with-caddy/1100)
- [React Router Authentication](https://reactrouter.com/en/main/start/concepts#authentication)
