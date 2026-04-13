-- +goose Up
-- Migration 004: Create store_logos storage bucket in Supabase
-- NOTE: Supabase Storage buckets are managed via the storage API, not SQL.
-- Run these commands in the Supabase SQL Editor:

-- 1. Create the bucket (public, 2MB max file size)
INSERT INTO storage.buckets (id, name, public, file_size_limit, allowed_mime_types)
VALUES (
  'store-logos',
  'store-logos',
  true,
  2097152, -- 2MB
  ARRAY['image/jpeg', 'image/png', 'image/webp', 'image/gif']
)
ON CONFLICT (id) DO NOTHING;

-- 2. Allow public read access to logos
CREATE POLICY "Public read store logos"
  ON storage.objects FOR SELECT
  USING (bucket_id = 'store-logos');

-- 3. Allow authenticated users to upload their own logos (folder = tenant_id)
CREATE POLICY "Authenticated upload store logos"
  ON storage.objects FOR INSERT
  WITH CHECK (
    bucket_id = 'store-logos'
    AND auth.role() = 'authenticated'
  );

-- 4. Allow authenticated users to update their own logos
CREATE POLICY "Authenticated update store logos"
  ON storage.objects FOR UPDATE
  USING (
    bucket_id = 'store-logos'
    AND auth.role() = 'authenticated'
  );

-- +goose Down
DELETE FROM storage.objects WHERE bucket_id = 'store-logos';
DELETE FROM storage.buckets WHERE id = 'store-logos';
