package grpcserver

import (
	"context"
	"strings"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// MasterAuthStreamInterceptor requires a valid Master JWT on streaming RPCs.
func MasterAuthStreamInterceptor(verifier *auth.Auth) grpc.StreamServerInterceptor {
	return func(
		srv any,
		stream grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		_ = info
		token, err := masterTokenFromContext(stream.Context())
		if err != nil {
			return err
		}
		if _, err := verifier.VerifyMasterJWT(token); err != nil {
			return status.Errorf(codes.Unauthenticated, "invalid master jwt: %v", err)
		}
		return handler(srv, stream)
	}
}

// MasterAuthUnaryInterceptor requires a valid Master JWT on gRPC metadata.
func MasterAuthUnaryInterceptor(verifier *auth.Auth) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		_ = info
		token, err := masterTokenFromContext(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := verifier.VerifyMasterJWT(token); err != nil {
			return nil, status.Errorf(codes.Unauthenticated, "invalid master jwt: %v", err)
		}
		return handler(ctx, req)
	}
}

func masterTokenFromContext(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return "", status.Error(codes.Unauthenticated, "missing authorization metadata")
	}
	value := strings.TrimSpace(values[0])
	if len(value) < 8 || !strings.EqualFold(value[:7], "bearer ") {
		return "", status.Error(codes.Unauthenticated, "authorization must be Bearer token")
	}
	token := strings.TrimSpace(value[7:])
	if token == "" {
		return "", status.Error(codes.Unauthenticated, "empty bearer token")
	}
	return token, nil
}
