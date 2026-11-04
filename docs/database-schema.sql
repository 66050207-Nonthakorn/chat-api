-- Design v1; not an applied or integration-tested migration.
-- Application creates UUIDs. PostgreSQL database encoding must be UTF8.
BEGIN;

CREATE TABLE app_users (
    user_id uuid PRIMARY KEY,
    display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 100),
    is_active boolean NOT NULL DEFAULT true,
    auth_epoch bigint NOT NULL DEFAULT 0 CHECK (auth_epoch >= 0),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_identities (
    identity_id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES app_users(user_id),
    issuer text NOT NULL CHECK (issuer <> ''),
    subject text NOT NULL CHECK (subject <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (issuer, subject),
    UNIQUE (identity_id, user_id)
);
CREATE INDEX user_identities_user_idx ON user_identities(user_id);

CREATE TABLE app_sessions (
    session_id uuid PRIMARY KEY,
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    csrf_token_hash bytea NOT NULL CHECK (octet_length(csrf_token_hash) = 32),
    user_id uuid NOT NULL REFERENCES app_users(user_id),
    identity_id uuid NOT NULL,
    auth_epoch bigint NOT NULL CHECK (auth_epoch >= 0),
    tokens_ciphertext bytea NOT NULL,
    encryption_key_id text NOT NULL,
    token_expires_at timestamptz NOT NULL,
    refresh_generation bigint NOT NULL DEFAULT 0 CHECK (refresh_generation >= 0),
    refresh_lease_token uuid,
    refresh_lease_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    idle_expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    FOREIGN KEY (identity_id, user_id)
        REFERENCES user_identities(identity_id, user_id),
    CHECK (expires_at > created_at),
    CHECK (idle_expires_at > created_at AND idle_expires_at <= expires_at),
    CHECK ((refresh_lease_token IS NULL) = (refresh_lease_until IS NULL))
);
CREATE INDEX app_sessions_user_idx ON app_sessions(user_id, session_id);
CREATE INDEX app_sessions_expiry_idx ON app_sessions(expires_at);
CREATE INDEX app_sessions_idle_expiry_idx ON app_sessions(idle_expires_at);

CREATE TABLE oauth_login_attempts (
    state_hash bytea PRIMARY KEY CHECK (octet_length(state_hash) = 32),
    browser_binding_hash bytea NOT NULL CHECK (octet_length(browser_binding_hash) = 32),
    nonce_hash bytea NOT NULL CHECK (octet_length(nonce_hash) = 32),
    pkce_verifier_ciphertext bytea NOT NULL,
    encryption_key_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    CHECK (expires_at > created_at)
);
CREATE INDEX oauth_login_attempts_expiry_idx ON oauth_login_attempts(expires_at);

CREATE TABLE rooms (
    room_id uuid PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('dm', 'group')),
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    owner_user_id uuid REFERENCES app_users(user_id),
    dm_user_low uuid REFERENCES app_users(user_id),
    dm_user_high uuid REFERENCES app_users(user_id),
    last_seq bigint NOT NULL DEFAULT 0 CHECK (last_seq >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (
        (kind = 'group' AND owner_user_id IS NOT NULL
          AND dm_user_low IS NULL AND dm_user_high IS NULL)
        OR
        (kind = 'dm' AND owner_user_id IS NULL
          AND dm_user_low IS NOT NULL AND dm_user_high IS NOT NULL
          AND dm_user_low < dm_user_high)
    )
);
CREATE UNIQUE INDEX rooms_dm_pair_idx ON rooms(dm_user_low, dm_user_high)
    WHERE kind = 'dm';
CREATE INDEX rooms_owner_idx ON rooms(owner_user_id) WHERE owner_user_id IS NOT NULL;
CREATE INDEX rooms_dm_high_idx ON rooms(dm_user_high) WHERE kind = 'dm';

CREATE TABLE room_memberships (
    room_id uuid NOT NULL REFERENCES rooms(room_id),
    user_id uuid NOT NULL REFERENCES app_users(user_id),
    member_role text NOT NULL DEFAULT 'member' CHECK (member_role IN ('member', 'admin')),
    joined_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (room_id, user_id)
);
CREATE INDEX room_memberships_user_room_idx ON room_memberships(user_id, room_id);
ALTER TABLE rooms ADD CONSTRAINT rooms_owner_membership_fk
    FOREIGN KEY (room_id, owner_user_id)
    REFERENCES room_memberships(room_id, user_id)
    DEFERRABLE INITIALLY DEFERRED;

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
    ON image_upload_candidates(state, created_at, image_id) WHERE state <> 'attached';

CREATE TABLE messages (
    message_id uuid PRIMARY KEY,
    room_id uuid NOT NULL REFERENCES rooms(room_id),
    seq bigint NOT NULL CHECK (seq > 0),
    sender_user_id uuid NOT NULL REFERENCES app_users(user_id),
    sender_display_name text NOT NULL CHECK (char_length(sender_display_name) BETWEEN 1 AND 100),
    client_message_id uuid NOT NULL,
    text text NOT NULL CHECK (octet_length(text) BETWEEN 0 AND 4096),
    image_id uuid REFERENCES image_upload_candidates(image_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT messages_text_or_image_check CHECK (octet_length(text) > 0 OR image_id IS NOT NULL),
    CONSTRAINT messages_room_seq_key UNIQUE (room_id, seq),
    CONSTRAINT messages_idempotency_key UNIQUE (sender_user_id, room_id, client_message_id)
);
CREATE INDEX messages_sender_idx ON messages(sender_user_id);
CREATE UNIQUE INDEX messages_image_id_key ON messages(image_id) WHERE image_id IS NOT NULL;

CREATE TABLE message_outbox (
    outbox_id uuid PRIMARY KEY,
    message_id uuid NOT NULL UNIQUE REFERENCES messages(message_id),
    event_type text NOT NULL DEFAULT 'message.created' CHECK (event_type = 'message.created'),
    schema_version smallint NOT NULL DEFAULT 1 CHECK (schema_version = 1),
    available_at timestamptz NOT NULL DEFAULT now(),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    lease_token uuid,
    lease_until timestamptz,
    published_at timestamptz,
    last_error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((lease_token IS NULL) = (lease_until IS NULL)),
    CHECK (published_at IS NULL OR lease_token IS NULL)
);
CREATE INDEX message_outbox_pending_idx
    ON message_outbox(available_at, outbox_id) WHERE published_at IS NULL;
CREATE INDEX message_outbox_lease_idx
    ON message_outbox(lease_until) WHERE published_at IS NULL AND lease_until IS NOT NULL;
CREATE INDEX message_outbox_published_idx
    ON message_outbox(published_at) WHERE published_at IS NOT NULL;

COMMIT;
