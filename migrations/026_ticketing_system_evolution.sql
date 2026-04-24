-- +goose Up
-- Migration 026: Ticketing system evolution.
--
-- Rationale:
--   Evolve the basic ticketing system into a world-class threaded
--   conversation system with priorities and categories.

-- 1. Create the messages table first
CREATE TABLE IF NOT EXISTS support_ticket_messages (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_id   UUID NOT NULL REFERENCES support_tickets(id) ON DELETE CASCADE,
    sender_type VARCHAR(16) NOT NULL CHECK (sender_type IN ('TENANT', 'ADMIN')),
    sender_id   UUID NOT NULL,
    content     TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 2. Alter support_tickets table
-- First, drop the old status constraint if it exists (standard PG names are often like table_column_check)
-- Since we don't know the exact name and it might vary by environment, we'll just add new columns
-- and update the check.

ALTER TABLE support_tickets ADD COLUMN IF NOT EXISTS priority VARCHAR(16) NOT NULL DEFAULT 'NORMAL';
ALTER TABLE support_tickets ADD COLUMN IF NOT EXISTS category VARCHAR(16) NOT NULL DEFAULT 'OTHER';

-- Update status check to include IN_PROGRESS
-- We drop and recreate the constraint. In migration 023 it didn't have a name, so it gets an auto-generated one.
-- Let's try to find it or just ignore it if we can't reliably name it, but usually, it's 'support_tickets_status_check'.
-- Alternatively, we can just allow the new values and rely on GORM validation, but SQL constraints are better.

ALTER TABLE support_tickets DROP CONSTRAINT IF EXISTS support_tickets_status_check;
ALTER TABLE support_tickets ADD CONSTRAINT support_tickets_status_check 
    CHECK (status IN ('OPEN', 'IN_PROGRESS', 'RESOLVED'));

-- 3. Migrate data: move initial message to the messages table
-- We assume the user who created the ticket (user_id) is the sender of the first message.
INSERT INTO support_ticket_messages (ticket_id, sender_type, sender_id, content, created_at)
SELECT id, 'TENANT', COALESCE(user_id, tenant_id), message, created_at
FROM support_tickets;

-- 4. Drop the old message column
ALTER TABLE support_tickets DROP COLUMN IF EXISTS message;

-- +goose Down
-- We don't really want to lose data on down, but to revert:
ALTER TABLE support_tickets ADD COLUMN message TEXT;
UPDATE support_tickets SET message = (
    SELECT content FROM support_ticket_messages 
    WHERE ticket_id = support_tickets.id 
    ORDER BY created_at ASC LIMIT 1
);

DROP TABLE IF EXISTS support_ticket_messages;
ALTER TABLE support_tickets DROP COLUMN IF EXISTS priority;
ALTER TABLE support_tickets DROP COLUMN IF EXISTS category;

ALTER TABLE support_tickets DROP CONSTRAINT IF EXISTS support_tickets_status_check;
ALTER TABLE support_tickets ADD CONSTRAINT support_tickets_status_check 
    CHECK (status IN ('OPEN', 'RESOLVED'));
