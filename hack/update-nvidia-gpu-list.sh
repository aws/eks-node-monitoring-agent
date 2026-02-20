#!/usr/bin/env bash
set -euo pipefail

# Get list of opted-in regions
mapfile -t REGIONS < <(
    aws ec2 describe-regions \
        --query 'Regions[?OptInStatus==`opt-in-not-required` || OptInStatus==`opted-in`].RegionName' \
        --output text \
    | tr '\t' '\n'
)

# Hard code just preview instance types or ones that may not show up in Describe responses
ALL_TYPES=("p6e-gb200.36xlarge" "p6e-gb300.36xlarge" "p6-b300.48xlarge" "p6-b200.48xlarge") 

# Fetch instance types from each region and filter by Manufacturer=NVIDIA
for REGION in "${REGIONS[@]}"; do
    echo "Getting NVIDIA GPU instance types in $REGION"
    mapfile -t TYPES < <(aws ec2 describe-instance-types \
        --region "$REGION" \
        --query 'InstanceTypes[?GpuInfo.Gpus[?Manufacturer==`NVIDIA`]].InstanceType' \
        --output text \
        | tr '\t' '\n' \
        | sed '/^$/d')
    
    if [[ ${#TYPES[@]} -gt 0 ]]; then
        ALL_TYPES+=("${TYPES[@]}")
    fi
done

# Path to the values.yaml file
VALUES_YAML="charts/eks-node-monitoring-agent/values.yaml"

if [[ ! -f "$VALUES_YAML" ]]; then
    echo "Error: $VALUES_YAML not found. Make sure you're running this script from the repository root."
    exit 1
fi

# Extract existing NVIDIA GPU instance types from values.yaml using yq
echo "Extracting existing NVIDIA GPU instance types from $VALUES_YAML..."
mapfile -t EXISTING_TYPES < <(yq eval '.dcgmAgent.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].values[]' "$VALUES_YAML")

echo "Found ${#EXISTING_TYPES[@]} existing instance types in values.yaml"

# Combine existing types with new types from AWS
echo "Combining existing types with new AWS types..."
ALL_COMBINED_TYPES=("${EXISTING_TYPES[@]}" "${ALL_TYPES[@]}")

# Deduplicate and sort the final list
FINAL_TYPES=($(printf "%s\n" "${ALL_COMBINED_TYPES[@]}" | sort -u))

echo ""
echo "=== UPDATING VALUES.YAML ==="
echo "Total unique instance types: ${#FINAL_TYPES[@]}"

# Use yq to find the exact line number, then use sed for surgical replacement
echo "Finding the exact line to update..."

# Get the line number of the values array using yq's line number feature
LINE_NUM=$(yq eval '.dcgmAgent.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0] | line' "$VALUES_YAML" | tail -1)

if [[ -z "$LINE_NUM" || "$LINE_NUM" == "null" ]]; then
    echo "Error: Could not find the NVIDIA GPU instance types location in $VALUES_YAML"
    exit 1
fi

# Find the values line after the key line (it should be the next line with "values:")
VALUES_LINE_NUM=$(sed -n "${LINE_NUM},\$p" "$VALUES_YAML" | grep -n "values:" | head -1 | cut -d: -f1)
ACTUAL_LINE_NUM=$((LINE_NUM + VALUES_LINE_NUM - 1))

echo "Found values array at line $ACTUAL_LINE_NUM"

# Create the new values line with proper indentation (12 spaces to match original)
NEW_VALUES_LINE="            values: [$(printf "%s\n" "${FINAL_TYPES[@]}" | paste -sd, - | sed 's/,/, /g')]"

echo "Updating line $ACTUAL_LINE_NUM with surgical sed replacement..."

# Replace only that specific line, preserving all other formatting
sed -i "${ACTUAL_LINE_NUM}s/.*/${NEW_VALUES_LINE}/" "$VALUES_YAML"

echo "Successfully updated $VALUES_YAML!"
