CREATE TABLE image_upload_candidates (
    image_id uuid PRIMARY KEY,
    room_id uuid NOT NULL REFERENCES rooms(room_id),
    sender_user_id uuid NOT NULL REFERENCES app_users(user_id),
    client_message_id uuid NOT NULL,
    source_sha256 bytea NOT NULL CHECK (octet_length(source_sha256) = 32),
    source_bytes integer NOT NULL CHECK (source_bytes BETWEEN 1 AND 5242880),
    source_content_type text NOT NULL CHECK (source_content_type IN ('image/jpeg', 'image/png', 'image/webp')),
    object_key text NOT NULL UNIQUE,
    content_type text NOT NULL CHECK (content_type IN ('image/jpeg', 'image/png')),
    byte_size integer NOT NULL CHECK (byte_size BETWEEN 1 AND 5242880),
    width integer NOT NULL CHECK (width BETWEEN 1 AND 2560),
    height integer NOT NULL CHECK (height BETWEEN 1 AND 2560),
    state text NOT NULL DEFAULT 'candidate' CHECK (state IN ('candidate', 'attached', 'deleting')),
    cleanup_after timestamptz,
    object_deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX image_upload_candidates_cleanup_idx
    ON image_upload_candidates (state, created_at, image_id) WHERE state <> 'attached';

ALTER TABLE messages ADD COLUMN image_id uuid REFERENCES image_upload_candidates(image_id);
CREATE UNIQUE INDEX messages_image_id_key ON messages(image_id) WHERE image_id IS NOT NULL;
ALTER TABLE messages DROP CONSTRAINT messages_text_check;
ALTER TABLE messages ADD CONSTRAINT messages_text_or_image_check
    CHECK (octet_length(text) BETWEEN 0 AND 4096 AND (octet_length(text) > 0 OR image_id IS NOT NULL));
