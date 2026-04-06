#!/usr/bin/env python3
"""
Start a packet capture on an EKS managed node.

Generates a pre-signed S3 POST, creates a NodeDiagnostic resource, and applies
it to the cluster. Requires: boto3, pyyaml, kubectl configured for the cluster.

Usage:
    python3 start-capture.py --bucket my-bucket --node i-0abc123

    # With options:
    python3 start-capture.py --bucket my-bucket --node i-0abc123 \
        --duration 5m --interface eth0 --filter "tcp port 443" --chunk-size 1

Environment:
    AWS credentials must be configured (e.g. via aws configure, IAM role, or
    environment variables) with permission to generate pre-signed POSTs for
    the target S3 bucket.
"""

import argparse
import subprocess
import sys

try:
    import boto3
except ImportError:
    print("Error: boto3 is required. Install with: pip install boto3", file=sys.stderr)
    sys.exit(1)

try:
    import yaml
except ImportError:
    print("Error: pyyaml is required. Install with: pip install pyyaml", file=sys.stderr)
    sys.exit(1)


def main():
    parser = argparse.ArgumentParser(
        description="Start a packet capture on an EKS managed node"
    )
    parser.add_argument("--bucket", required=True, help="S3 bucket for capture uploads")
    parser.add_argument("--node", required=True, help="Node name (instance ID)")
    parser.add_argument("--prefix", default=None, help="S3 key prefix (default: captures/<node>/)")
    parser.add_argument("--duration", default="30s", help="Capture duration (default: 30s, max: 1h)")
    parser.add_argument("--interface", default="", help="Network interface (default: tcpdump default, use 'any' for all interfaces)")
    parser.add_argument("--filter", default="", help='tcpdump filter (e.g. "tcp port 443")')
    parser.add_argument("--chunk-size", type=int, default=10, help="Rotation size in MB (default: 10)")
    parser.add_argument("--expiration", type=int, default=3600, help="Pre-signed URL expiration in seconds (default: 3600)")
    parser.add_argument("--dry-run", action="store_true", help="Print YAML without applying")
    args = parser.parse_args()

    prefix = args.prefix or f"captures/{args.node}/"
    if not prefix.endswith("/"):
        prefix += "/"

    # Generate pre-signed POST
    presign = boto3.client("s3").generate_presigned_post(
        Bucket=args.bucket,
        Key=prefix + "${filename}",
        Conditions=[["starts-with", "$key", prefix]],
        ExpiresIn=args.expiration,
    )

    # Build spec
    spec = {"duration": args.duration, "upload": presign}
    if args.interface:
        spec["interface"] = args.interface
    if args.filter:
        spec["filter"] = args.filter
    if args.chunk_size != 10:
        spec["chunkSizeMB"] = args.chunk_size

    nd = {
        "apiVersion": "eks.amazonaws.com/v1alpha1",
        "kind": "NodeDiagnostic",
        "metadata": {"name": args.node},
        "spec": {"packetCapture": spec},
    }

    manifest = yaml.dump(nd, default_flow_style=False)

    if args.dry_run:
        print(manifest)
        return

    print(manifest)
    result = subprocess.run(
        ["kubectl", "apply", "-f", "-"],
        input=manifest.encode(),
        capture_output=True,
    )
    sys.stdout.buffer.write(result.stdout)
    sys.stderr.buffer.write(result.stderr)

    if result.returncode != 0:
        sys.exit(result.returncode)

    print(f"\nCapture started on node {args.node}. Monitor with:")
    print(f"  kubectl describe nodediagnostic {args.node}")
    print(f"\nWhen complete, download files with:")
    print(f"  aws s3 cp s3://{args.bucket}/{prefix} ./captures/ --recursive")


if __name__ == "__main__":
    main()
