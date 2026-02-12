package utils

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
	"gitlab.cloudferro.com/k8s/api/clusterservice/v1"
	cferror "gitlab.cloudferro.com/k8s/api/error/v1"
	"google.golang.org/grpc"
)

func WaitForClusterToNotBeBusy(ctx context.Context, cli *grpc.ClientConn, clusterID string) error {
	clusterCli := clusterservice.NewClusterClient(cli)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {

		cluster, err := clusterCli.GetCluster(ctx, &clusterservice.GetClusterRequest{
			ClusterId: clusterID,
		})
		if err != nil {
			return err
		}

		if cluster.Status == "Running" || cluster.Status == "Error" {
			return nil
		}

		tflog.Debug(ctx, "cluster is busy, waiting for it to be not busy")

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			continue
		}
	}
}

func GetLatestError(ctx context.Context, cli *grpc.ClientConn, clusterID string) (*cferror.Error, error) {
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
