CREATE TABLE room_requests (
    user_id uuid NOT NULL REFERENCES app_users(user_id),
    operation text NOT NULL CHECK (operation IN ('create_group', 'add_members')),
    request_id uuid NOT NULL,
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    room_id uuid NOT NULL REFERENCES rooms(room_id),
    PRIMARY KEY (user_id, operation, request_id)
);
