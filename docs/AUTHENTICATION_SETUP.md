# KubeBrowse Authentication Setup Guide

This guide explains how to set up GitHub OAuth and email-based authentication for KubeBrowse.

## Features

- **GitHub OAuth**: Sign in with GitHub account
- **Email/Password Authentication**: Traditional email and password sign-in
- **Server-side Sessions**: Secure session management with database persistence
- **Frontend Integration**: React components with authentication context
- **Session Management**: Automatic session validation and cleanup

## Prerequisites

1. **Database**: PostgreSQL database for user and session storage
2. **GitHub OAuth App**: GitHub OAuth application for OAuth authentication
3. **Environment Variables**: Properly configured environment variables

## Setup Instructions

### 1. Database Setup

#### Create Database
```bash
# Connect to PostgreSQL
psql -U postgres

# Create database
CREATE DATABASE kubebrowse;

# Connect to the database
\c kubebrowse;
```

#### Run Migrations
```bash
# Apply database migrations
migrate -path db/migrations -database "postgres://user:password@localhost:5432/kubebrowse?sslmode=disable" up

# Or using your preferred migration tool
```

#### Generate SQLC Code
```bash
# Generate Go code from SQL queries
sqlc generate
```

### 2. GitHub OAuth Setup

#### Create GitHub OAuth App
1. Go to GitHub Settings → Developer settings → OAuth Apps
2. Click "New OAuth App"
3. Fill in the details:
   - **Application name**: KubeBrowse
   - **Homepage URL**: `http://localhost:3000` (or your domain)
   - **Authorization callback URL**: `http://localhost:4000/auth/oauth/github/callback`
4. Click "Register application"
5. Note down the **Client ID** and **Client Secret**

### 3. Environment Configuration

#### Backend (.env)
Create or update your `.env` file:

```bash
# Database Configuration
DATABASE_URL=postgres://postgresuser:postgrespassword@localhost:5432/kubebrowse?sslmode=disable

# GitHub OAuth Configuration
GITHUB_CLIENT_ID=your_github_client_id_here
GITHUB_CLIENT_SECRET=your_github_client_secret_here
GITHUB_CALLBACK_URL=http://localhost:4000/auth/oauth/github/callback

# Frontend URL for OAuth redirects
FRONTEND_URL=http://localhost:3000

# Session Configuration
SESSION_SECRET=your_long_random_session_secret_here_make_it_very_secure

# Other existing configuration...
GUACD_ADDRESS=localhost:4822
REDIS_HOST=localhost
# ... etc
```

#### Frontend (.env.local)
Create a `.env.local` file in the frontend directory:

```bash
VITE_API_BASE_URL=http://localhost:4000
```

### 4. Backend Setup

#### Install Dependencies

#### Build and Run
```bash
# Build the application
encore run

# Run the application
./guac
```

### 5. Frontend Setup

#### Install Dependencies
```bash
cd frontend
npm install
# or
pnpm install
```

#### Run Development Server
```bash
npm run dev
# or
pnpm dev
```

## API Endpoints

### Authentication Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/auth/register` | Register with email/password |
| POST | `/auth/login` | Login with email/password |
| POST | `/auth/logout` | Logout current user |
| GET | `/auth/oauth/github` | Start GitHub OAuth flow |
| GET | `/auth/oauth/github/callback` | GitHub OAuth callback |
| GET | `/auth/me` | Get current user info |

### Request/Response Examples

#### Register with Email
```bash
curl -X POST http://localhost:4000/auth/register \
  -H "Content-Type: application/json" \
  -d '{
    "email": "user@example.com",
    "password": "securepassword123"
  }'
```

#### Login with Email
```bash
curl -X POST http://localhost:4000/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "user@example.com",
    "password": "securepassword123"
  }'
```

#### Get Current User
```bash
curl -X GET http://localhost:4000/auth/me \
  -H "Cookie: kubebrowse_session=your_session_token"
```

## Frontend Usage

### Authentication Context

The authentication state is managed by `AuthContext` and can be accessed using the `useAuth` hook:

```jsx
import { useAuth } from '../context/AuthContext';

function MyComponent() {
  const { 
    user, 
    isAuthenticated, 
    isLoading, 
    login, 
    register, 
    logout, 
    loginWithGitHub 
  } = useAuth();

  if (isLoading) return <div>Loading...</div>;

  if (!isAuthenticated) {
    return <LoginForm />;
  }

  return (
    <div>
      <h1>Welcome, {user.name || user.email}!</h1>
      <button onClick={logout}>Logout</button>
    </div>
  );
}
```

### Authentication Components

- **`AuthModal`**: Modal dialog for login/register
- **`LoginForm`**: Email/password login form
- **`RegisterForm`**: Email/password registration form
- **`UserMenu`**: Dropdown menu for authenticated users

## Database Schema

### Users Table
### Sessions Table
```sql
CREATE TABLE user_sessions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  session_token VARCHAR(255) NOT NULL UNIQUE,
  expires_at TIMESTAMP NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);
```

## Security Considerations

1. **Session Tokens**: Use cryptographically secure random tokens
2. **Password Hashing**: Passwords are hashed using bcrypt with default cost
3. **HTTPS**: Use HTTPS in production for secure cookie transmission
4. **Session Expiration**: Sessions expire after 7 days by default
5. **CORS**: Configure CORS appropriately for your domain
6. **Environment Variables**: Never commit secrets to version control

## Troubleshooting

### Common Issues

1. **Database Connection Error**
   - Verify PostgreSQL is running
   - Check DATABASE_URL format
   - Ensure database exists and user has permissions

2. **GitHub OAuth Not Working**
   - Verify GITHUB_CLIENT_ID and GITHUB_CLIENT_SECRET
   - Check callback URL matches GitHub app settings
   - Ensure FRONTEND_URL is correct

3. **Session Not Persisting**
   - Check cookie settings (secure, httpOnly, domain)
   - Verify session secret is set
   - Check session expiration times

4. **CORS Issues**
   - Verify CORS configuration allows your frontend domain
   - Check credentials: 'include' in frontend requests

### Debug Mode

Enable debug logging:
```bash
export LOG_LEVEL=debug
./guac
```

## Production Deployment

### Environment Variables for Production
```bash
# Use secure values in production
DATABASE_URL=postgres://user:password@prod-db:5432/kubebrowse?sslmode=require
GITHUB_CLIENT_ID=prod_github_client_id
GITHUB_CLIENT_SECRET=prod_github_client_secret
GITHUB_CALLBACK_URL=https://yourdomain.com/auth/oauth/github/callback
FRONTEND_URL=https://yourdomain.com
SESSION_SECRET=very_long_random_string_for_production
```

### Security Checklist
- [ ] Use HTTPS for all communications
- [ ] Set secure session cookie flags
- [ ] Use strong, unique session secrets
- [ ] Configure proper CORS settings
- [ ] Set up database connection pooling
- [ ] Enable database SSL/TLS
- [ ] Set up proper logging and monitoring
- [ ] Configure rate limiting for auth endpoints

## Support

For issues and questions:
1. Check the troubleshooting section
2. Review the logs for error messages
3. Verify environment configuration
4. Check database connectivity and migrations
