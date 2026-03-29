#!/usr/bin/env bash
set -euo pipefail

# ── VendIA Backend — GCP Cloud Run Deploy (Free-Tier Protected) ────────────────
PROJECT_ID="${GCP_PROJECT_ID:?Set GCP_PROJECT_ID env var}"
SERVICE_NAME="vendia-backend"
REGION="us-central1"
IMAGE="gcr.io/${PROJECT_ID}/${SERVICE_NAME}"

echo "▶ Building and pushing Docker image..."
gcloud builds submit --tag "${IMAGE}" .

echo "▶ Deploying to Cloud Run (free-tier safe)..."
gcloud run deploy "${SERVICE_NAME}" \
  --image "${IMAGE}" \
  --platform managed \
  --region "${REGION}" \
  --allow-unauthenticated \
  --max-instances 2 \
  --memory 256Mi \
  --cpu 1 \
  --concurrency 80 \
  --timeout 30s \
  --set-env-vars "DATABASE_URL=${DATABASE_URL:?Set DATABASE_URL env var},JWT_SECRET=${JWT_SECRET:?Set JWT_SECRET env var}" \
  --project "${PROJECT_ID}"

echo ""
echo "✅ Deploy complete. Service URL:"
gcloud run services describe "${SERVICE_NAME}" \
  --region "${REGION}" \
  --format "value(status.url)"
