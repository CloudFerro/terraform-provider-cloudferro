package utils

import (
	"context"

	"gitlab.cloudferro.com/k8s/api/clusterservice/v1"
	cferror "gitlab.cloudferro.com/k8s/api/error/v1"
	"google.golang.org/grpc"
)

func GetLatestClusterError(ctx context.Context, cli *grpc.ClientConn, clusterID string) (*cferror.Error, error) {
	clusterCli := clusterservice.NewClusterClient(cli)

	resp, err := clusterCli.GetCluster(ctx, &clusterservice.GetClusterRequest{
		ClusterId:   clusterID,
		ExtraFields: "errors",
	})
	if err != nil {
		return nil, err
	}

	latestErr := &cferror.Error{}
	for _, er := range resp.Errors {
		if latestErr.CreatedAt.AsTime().Before(er.CreatedAt.AsTime()) {
			latestErr = er
		}
	}

	if latestErr.CreatedAt.AsTime().IsZero() {
		return nil, nil
	}

	return latestErr, nil
}
