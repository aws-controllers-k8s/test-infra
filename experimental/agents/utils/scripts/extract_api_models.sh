#!/bin/bash

# Define directories
REPO_URL="https://github.com/aws/api-models-aws.git"
CLONE_DIR="/tmp/api-models-aws"
TARGET_DIR="$(pwd)/api-models"

echo "Current directory: $(pwd)"
echo "Starting API models extraction script..."

# Create target directory if it doesn't exist
if [ ! -d "$TARGET_DIR" ]; then
  echo "Creating target directory: $TARGET_DIR"
  mkdir -p "$TARGET_DIR"
else
  echo "Target directory already exists: $TARGET_DIR"
fi

# Clone repository if it doesn't exist
if [ ! -d "$CLONE_DIR" ]; then
  echo "Cloning repository: $REPO_URL"
  git clone "$REPO_URL" "$CLONE_DIR"
else
  echo "Repository already cloned at: $CLONE_DIR"
  echo "Updating repository..."
  cd "$CLONE_DIR" && git pull && cd -
fi

# Check if models directory exists in the cloned repo
if [ ! -d "$CLONE_DIR/models" ]; then
  echo "Error: models directory not found in the cloned repository"
  exit 1
fi

echo "Copying JSON files from models directory to $TARGET_DIR..."
find "$CLONE_DIR/models" -name "*.json" -exec cp {} "$TARGET_DIR/" \;

# Count the number of copied files
JSON_COUNT=$(find "$TARGET_DIR" -name "*.json" | wc -l)
echo "Copied $JSON_COUNT JSON files to $TARGET_DIR"

# Clean up the cloned repository
echo "Cleaning up: removing cloned repository"
rm -rf "$CLONE_DIR"

echo "Script completed successfully!"
echo "All JSON files are now available in the '$TARGET_DIR' directory"

# Ask if user wants to upload to S3
read -p "Would you like to upload these files to an S3 bucket? (y/n): " upload_choice

if [[ $upload_choice == "y" || $upload_choice == "Y" ]]; then
  USERNAME=$(whoami)
  RANDOM_STRING=$(cat /dev/urandom | LC_ALL=C tr -dc 'a-z0-9' | fold -w 4 | head -n 1)
  BUCKET_NAME="api-model-agent-${USERNAME}--${RANDOM_STRING}"
  
  echo "Creating S3 bucket: $BUCKET_NAME"
  
  # Check if AWS CLI is installed
  if ! command -v aws &> /dev/null; then
    echo "AWS CLI is not installed. Please install it to upload files to S3."
    exit 1
  fi
  # Upload files
  aws s3api create-bucket --bucket $BUCKET_NAME --region us-west-2 --create-bucket-configuration LocationConstraint=us-west-2
  
  if [ $? -eq 0 ]; then
    echo "Bucket created successfully. Uploading files..."
    aws s3 sync $TARGET_DIR s3://$BUCKET_NAME/ --region us-west-2
    if [ $? -eq 0 ]; then
      echo "Files uploaded successfully to s3://$BUCKET_NAME/"
    else
      echo "Error uploading files to S3 bucket."
    fi
  else
    echo "Error creating S3 bucket. Please check your AWS credentials and permissions."
  fi
else
  echo "Skipping S3 upload."
fi

echo "Script execution complete."
