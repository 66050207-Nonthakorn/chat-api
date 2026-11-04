# สัญญา HTTP API และ WebSocket — เดโม Cloud Computing

สถานะ: **แบบออกแบบ v1 สำหรับนำไปพัฒนา** ยังไม่มี implementation หรือผลทดสอบยืนยัน ตัวเลขจำกัดด้านล่างเป็นค่าเริ่มต้นที่เสนอสำหรับเดโม ไม่ใช่ผลวัดหรือ SLA

อ้างอิง [ADR ข้อความ](adr/0001-durable-messaging-and-realtime.md), [ADR ตัวตน](adr/0002-identity-sessions-and-membership.md), [ADR ฝั่งเว็บ](adr/0004-client-state-and-deferred-features.md) และ [เกณฑ์ตรวจรับ](adr/references/02-implementation-and-acceptance.md)

## 1. ขอบเขตและกติกากลาง

- ใช้ origin เดียวผ่าน CloudFront: `/api/*` และ `/auth/*` ไป ALB, ไฟล์หน้าเว็บไป S3; การเชื่อมต่อสดใช้ `/api/v1/ws`
- HTTP ใช้ส่งข้อความและอ่านข้อมูล; WebSocket ใช้ติดตามห้องและรับเหตุการณ์ ไม่ใช้ส่งคำสั่งสร้างข้อความ
- เดโมใช้ผู้ใช้และห้องที่เตรียมไว้ ไม่มี API สมัครสาธารณะ สร้างห้อง จัดการสมาชิก read receipt, typing, presence, edit/delete หรือไฟล์แนบใน v1
- HTTP JSON ใช้ `Content-Type: application/json`; เวลาเป็น UTC RFC 3339 เช่น `2026-10-30T10:00:00Z`
- `user_id`, `room_id`, `message_id`, `client_message_id` เป็น UUID แบบ canonical lowercase; รหัส opaque อื่นห้ามตีความ
- `seq`, `after_seq`, `before_seq`, `through_seq`, `next_after_seq` เป็น **decimal string** เพื่อไม่ให้ JavaScript สูญเสียความแม่นยำ; ใช้ BigInt เปรียบเทียบ ไม่เรียงแบบข้อความ ตัวอย่าง `"0"`, `"42"` ไม่รับค่าติดลบหรือเลขศูนย์นำหน้า
- ลำดับข้อความเริ่มที่ 1 แยกต่อห้อง ไม่มีลำดับรวมทั้งแอป; `0` หมายถึงยังไม่ได้รับข้อความใด
- ใน v1 ข้อความเป็น immutable และ seq ที่ commit แล้วต้องต่อเนื่อง การ rollback ต้องไม่ใช้ seq ทิ้งถาวร จึงห้ามใช้ PostgreSQL sequence เพียงอย่างเดียวเพื่อรับประกันเงื่อนไขนี้
- ปฏิเสธฟิลด์คำขอที่ไม่รู้จัก; client ต้องรับฟิลด์ response ที่เพิ่มในอนาคตได้ การเปลี่ยนความหมาย/ลบฟิลด์ต้องใช้เวอร์ชันใหม่
- ทุก response ของ API/auth รวมข้อผิดพลาดใช้ `Cache-Control: no-store`; CloudFront ต้องปิด cache และส่ง cookie, query string, Origin และ header ที่แต่ละเส้นทางต้องใช้ครบ
- ทุก HTTP response มี `X-Request-ID` ที่เซิร์ฟเวอร์สร้าง ใช้เชื่อม log/trace; ไม่ใช้แทน idempotency key และไม่ log เนื้อหาข้อความ cookie หรือ token

## 2. ตัวตน Session และ CSRF

production ใช้ Cognito ผ่าน backend-managed session เท่านั้น Browser ไม่ได้รับ access/refresh token และห้ามรับ `user_id` จาก client เพื่อใช้เป็นตัวตน

ค่าเริ่มต้นที่เสนอ: session อายุสูงสุด 8 ชั่วโมง, idle timeout 30 นาที; คำขอ HTTP และ WebSocket ping ที่ผ่านการตรวจ session ต่ออายุ idle ได้ แต่ไม่ยืดอายุสูงสุด เก็บ session และการเพิกถอนใน PostgreSQL ใช้ได้ทุก replica

- Cookie: `__Host-chat_session=<opaque>; Path=/; Secure; HttpOnly; SameSite=Lax` ไม่ใส่ Domain
- `/auth/*` และ API ต้องใช้ HTTPS ใน environment จริง; local ใช้ HTTPS หรือ cookie สำหรับ local โดยเฉพาะที่เปิดบน AWS ไม่ได้
- คำขอเปลี่ยนข้อมูล `POST` ต้องมี `Origin` ตรง allowlist แบบ exact และ `X-CSRF-Token` ผูกกับ session; ไม่รับ Origin ที่หายหรือ `null`
- หน้าเว็บรับ CSRF token จาก `GET /api/v1/session` แล้วเก็บในหน่วยความจำ; CSRF token ไม่ใช่ token ของ Cognito
- ตรวจ session กับฐานข้อมูลทุกคำขอ รวม replay ของคำขอซ้ำ; ฐานข้อมูลใช้ไม่ได้ให้ตอบ 503 และไม่อนุญาตเข้าถึง
- ตรวจสิทธิ์สมาชิกปัจจุบันก่อนส่ง/อ่าน/subscribe/ส่งสดแต่ละครั้ง; ไม่อาศัย notification การลบสมาชิกอย่างเดียว
- socket ตรวจ session ก่อนแต่ละคำสั่งและก่อนส่งข้อมูลส่วนตัว พร้อมตรวจตามรอบไม่เกิน 30 วินาที เพื่อปิด socket ว่างที่ session ถูกเพิกถอน
- token ที่เก็บฝั่ง server ต้องเข้ารหัสและมี key rotation; ประสาน refresh ข้าม replica ด้วย lock/version ใน PostgreSQL ไม่ส่ง refresh token ไป browser

### เส้นทางเข้าสู่ระบบ

| Method / path | คำขอและผลลัพธ์ |
| --- | --- |
| `GET /auth/login` | สร้าง state, nonce และ PKCE ฝั่ง backend อายุ 5 นาที ผูกกับ browser; 302 ไป Cognito; ใช้ return path คงที่ `/` ใน v1 |
| `GET /auth/callback?code=…&state=…` | ตรวจ state/nonce/PKCE และใช้ครั้งเดียว; แลก token ฝั่ง backend, จับคู่ `(issuer, subject)` กับผู้ใช้ที่เตรียมไว้, rotate session, ตั้ง cookie แล้ว 303 ไป `/`; ผู้ไม่มีสิทธิ์เข้าแอปได้ 403 |
| `POST /auth/logout` | ต้องมี session/CSRF; revoke session เครื่องนี้ ล้าง cookie แล้ว 204; session ถูก revoke แล้วไม่ให้ socket ส่งข้อมูลเพิ่มหลังการตรวจครั้งถัดไป |
| `POST /auth/logout-all` | ต้องมี session/CSRF; revoke ทุก session ของผู้ใช้ปัจจุบัน ล้าง cookie แล้ว 204 |

Logout หมายถึงออกจาก session ของแอป การออกจาก SSO ของ Cognito เป็นคนละขั้น ไม่รับรองว่า login ครั้งใหม่จะถามรหัสผ่านเสมอ

`GET /api/v1/session` → 200:

```json
{
  "user": {"user_id": "11111111-1111-4111-8111-111111111111", "display_name": "Demo A"},
  "session": {"expires_at": "2026-10-30T18:00:00Z", "idle_expires_at": "2026-10-30T10:30:00Z"},
  "csrf_token": "opaque-session-bound-value"
}
```

ไม่มี/หมดอายุ/ถูกเพิกถอน session → 401; frontend หยุด retry ข้อมูลส่วนตัว ปิด socket และให้เข้าสู่ระบบใหม่ แยก pending data ตาม user_id และล้างเมื่อ logout; ถ้า IndexedDB ใช้ไม่ได้ให้แจ้งว่าคง pending ข้ามการโหลดหน้าไม่ได้

## 3. รูปแบบข้อมูลร่วม

### Room

```json
{
  "room_id": "22222222-2222-4222-8222-222222222222",
  "kind": "group",
  "title": "Cloud demo",
  "membership_role": "member",
  "last_seq": "42"
}
```

`kind`: `dm | group`; `membership_role`: `owner | admin | member`; DM ใช้ `member` ทั้งคู่ `last_seq` เป็น snapshot ขณะอ่าน ไม่ใช่หลักฐานว่า client ได้รับข้อความครบ

### Message

```json
{
  "message_id": "33333333-3333-4333-8333-333333333333",
  "room_id": "22222222-2222-4222-8222-222222222222",
  "seq": "42",
  "sender": {"user_id": "11111111-1111-4111-8111-111111111111", "display_name": "Demo A"},
  "client_message_id": "44444444-4444-4444-8444-444444444444",
  "text": "hello",
  "created_at": "2026-10-30T10:00:00Z"
}
```

- เซิร์ฟเวอร์กำหนด message_id, seq, sender และ created_at; ชื่อผู้ส่งเป็น snapshot ตอนสร้างข้อความ จึงได้ผลเดิมเมื่อ replay
- `text`: Unicode ที่ถูกต้อง มีอักขระที่ไม่ใช่ whitespace อย่างน้อย 1 ตัว, ไม่รับ U+0000, ขนาด UTF-8 ไม่เกิน 4,096 bytes; ไม่ trim หรือ normalize เนื้อหาเงียบ ๆ
- frontend แสดงเป็น plain text ไม่ตีความเป็น HTML; ใช้ message_id ตัดซ้ำและ seq จัดลำดับ ไม่ใช้เวลาหรือเวลาที่ event มาถึง

## 4. HTTP endpoints

ดูขั้นตอนส่ง/รับรูป, retry, runtime limits และ release ใน [คู่มือ image messages](image-messages.md) พร้อม [Bruno collection](../bruno/cloud-chat-local/README.md)

ทุก endpoint ในส่วนนี้ต้องมี session; POST ต้องมี CSRF เพิ่มด้วย

| Method / path | หน้าที่ | ผลสำเร็จ |
| --- | --- | --- |
| `GET /api/v1/rooms?limit=50&cursor=…` | ห้องที่ผู้ใช้เป็นสมาชิกปัจจุบัน | 200 `{items: Room[], next_cursor: string หรือ null}` |
| `POST /api/v1/rooms/{room_id}/messages` | บันทึกข้อความพร้อม outbox; JSON สำหรับข้อความ และ multipart สำหรับรูป | 201 เมื่อสร้างใหม่, 200 เมื่อ replay |
| `GET /api/v1/rooms/{room_id}/messages?limit=50&before_seq=…` | ประวัติจากใหม่ไปเก่า | 200 ตามรูปแบบด้านล่าง |
| `GET /api/v1/rooms/{room_id}/sync?after_seq=0&limit=100&through_seq=…` | ดึงช่วงที่พลาดจากเก่าไปใหม่ | 200 ตามรูปแบบด้านล่าง |
| `GET /api/v1/rooms/{room_id}/messages/{message_id}/image` | อ่านรูปผ่าน API หลังตรวจสมาชิกปัจจุบัน | 200 รูปที่ผ่านการประมวลผล |

ห้องไม่มีอยู่หรือผู้ใช้ไม่เป็นสมาชิกตอบ 404 แบบเดียวกันเพื่อไม่เปิดเผยว่าห้องมีอยู่ ส่วน role/การจัดการสมาชิกยังไม่มี endpoint ในเดโม; การทดสอบลบสมาชิกใช้ fixture/admin script ที่ควบคุมสิทธิ์

### รายการห้อง

`limit` 1–100 ค่าเริ่มต้น 50; เรียง room_id จากน้อยไปมาก ใช้ opaque keyset cursor ที่ server ตรวจความถูกต้องและผูกกับผู้ใช้ ไม่ใช่ offset; cursor ผิดตอบ 400 `INVALID_CURSOR` แต่ละหน้าตรวจสมาชิกใหม่ การเปลี่ยนสมาชิกระหว่างแบ่งหน้าอาจเปลี่ยนรายการ จึง refresh จากหน้าแรกได้

### ส่งข้อความและ idempotency

```http
POST /api/v1/rooms/22222222-2222-4222-8222-222222222222/messages
Content-Type: application/json
X-CSRF-Token: opaque-session-bound-value
```

```json
{"client_message_id":"44444444-4444-4444-8444-444444444444","text":"hello"}
```

ผลลัพธ์ใช้ envelope เดียวกันทั้ง 201 และ 200:

```json
{"message":{"message_id":"33333333-3333-4333-8333-333333333333","room_id":"22222222-2222-4222-8222-222222222222","seq":"42","sender":{"user_id":"11111111-1111-4111-8111-111111111111","display_name":"Demo A"},"client_message_id":"44444444-4444-4444-8444-444444444444","text":"hello","created_at":"2026-10-30T10:00:00Z"},"replayed":false}
```

กติกา:

1. Browser สร้าง client_message_id และเก็บ pending ก่อนส่ง ใช้รหัสเดิมเมื่อ timeout/โหลดใหม่/ลองซ้ำ ไม่สร้างรหัสใหม่เพียงเพราะไม่ทราบผล
2. Unique key คือ `(sender_user_id, room_id, client_message_id)` เก็บตลอดอายุข้อมูลโครงการ
3. รหัสเดิมและ text เดิมแบบตรงตัวหลัง JSON decode → ข้อความเดิม, 200 และ `replayed: true`; text ต่าง → 409 `IDEMPOTENCY_CONFLICT` เปรียบเทียบเนื้อหาที่บันทึกจริง ไม่ใช้ hash อย่างเดียว
4. ตรวจ session/สมาชิกก่อน replay ด้วย ผู้ถูกลบจากห้องไม่ได้รับข้อมูลเดิมผ่าน endpoint นี้
5. ใน transaction เดียว: ตรวจสมาชิกตามกฎ lock ร่วมกับการลบสมาชิก → จัด seq → INSERT message + outbox → commit → ตอบผล ไม่รอ Pub/Sub หรือผู้รับ
6. ใช้ unique constraint และจัดการการแข่งขัน ไม่ใช้แค่ query ก่อน insert; คำขอพร้อมกัน 100 ครั้งต้องมีข้อความเดียว
7. transaction rollback ต้องไม่มีทั้ง message และ outbox; ถ้าผล commit ไม่แน่นอนให้ตอบข้อผิดพลาด/ตัดการเชื่อมต่อ ห้ามตอบสำเร็จโดยเดา client retry รหัสเดิม
8. HTTP timeout, network error หรือ 5xx อาจเกิดหลัง commit จึงไม่แปลว่าข้อความไม่ถูกบันทึก
9. รับ message.created ของตัวเองก่อน HTTP ได้: รวมกับ pending ผ่าน sender_user_id + room_id + client_message_id แล้วตัดซ้ำด้วย message_id ข้อความสดจาก server เป็นหลักฐานของข้อความที่ commit แล้ว แต่ไม่ใช่หลักฐานว่าผู้รับอ่านแล้ว

สำหรับรูป ส่ง `multipart/form-data` ไปยัง path เดิม โดยมี `client_message_id`, `image` หนึ่งไฟล์ และ `text` ซึ่งไม่บังคับ (caption ว่างได้) รูปต้นทางรับ JPEG, PNG หรือ WebP ไม่เกิน 5 MiB; request รวมไม่เกิน 6 MiB Backend ตรวจชนิดจริงและส่งกลับเฉพาะรูปที่ปรับ orientation, ลดด้านยาวไม่เกิน 2,560 px และ encode ใหม่เพื่อตัด metadata รูปที่ส่งแล้วมี `image` เพิ่มใน Message: `{ "url": "/api/v1/rooms/{room_id}/messages/{message_id}/image", "content_type": "image/jpeg หรือ image/png", "byte_size": 1234, "width": 800, "height": 600 }` URL นี้เป็น route ที่ต้องมี session และเป็นสมาชิกห้องขณะอ่าน; response ไม่เปิดเผย object key, ส่ง `Cache-Control: no-store` และ `X-Content-Type-Options: nosniff`

ใน production `IMAGE_MESSAGES_ENABLED` ปิดไว้จน API และ worker รุ่นที่รองรับ image metadata deploy ครบแล้ว จากนั้นเปิดผ่าน Terraform `enable_image_messages` แยกหลัง rollout

Upload ใช้เวลาได้สูงสุด 45 วินาทีและมี read deadline จริง; API task รับ image pipeline พร้อมกันหนึ่งคำขอ หากเต็มตอบ 429 `IMAGE_UPLOAD_BUSY` พร้อม `Retry-After: 1` โดยไม่รอคิว เมื่ออ่าน body หมดเวลาตอบ 408 `UPLOAD_TIMEOUT` ให้ retry ด้วย client_message_id เดิม Download ตรวจสิทธิ์และขนาด object ก่อน stream โดยจำกัดเวลา 10 วินาที; หากขาดกลางทางให้ retry เฉพาะ GET รูป ไม่สร้างข้อความใหม่

การ replay รูปใช้ client_message_id เดิมได้เมื่อ caption และ fingerprint ของไฟล์ต้นทางตรงกัน; เนื้อหาที่ต่างกันตอบ 409 `IDEMPOTENCY_CONFLICT` เช่นเดียวกับข้อความ การอ่านประวัติ, sync และ `message.created` ใช้ Message รูปแบบเดียวกัน โดยส่ง metadata และ URL เท่านั้น ไม่ส่ง binary ผ่าน WebSocket

คำตอบสำเร็จหมายถึงบันทึกในฐานข้อมูลแล้วเท่านั้น ไม่มีสถานะ delivered/read ใน v1

### อ่านประวัติ

`limit` 1–100 ค่าเริ่มต้น 50; ไม่ส่ง before_seq หมายถึงหน้าล่าสุด; ถ้าส่งให้เลือก `seq < before_seq` และเรียง seq ลดลง

```json
{"items":[],"next_before_seq":null,"has_more":false}
```

เมื่อมีข้อมูล `items` เป็น Message[]; ถ้ายังมีหน้าถัดไป next_before_seq เท่ากับ seq ต่ำสุดในหน้าปัจจุบัน ไม่เช่นนั้นเป็น null ประวัติที่แสดงบนจอสามารถกลับลำดับเป็นเก่าไปใหม่ได้

**ห้ามใช้หน้าประวัติล่าสุดเลื่อน recovery cursor ข้ามช่วงที่ยังไม่ได้ดึง** v1 เริ่ม sync จาก 0 หรือ cursor ต่อเนื่องที่เก็บไว้เดิม และดึงจนถึง snapshot ก่อนถือว่ากู้ครบ

### ดึงข้อความที่พลาด

- `after_seq` จำเป็นและเป็นขอบล่างแบบไม่รวม; `limit` 1–200 ค่าเริ่มต้น 100
- ถ้าไม่ส่ง `through_seq` server อ่าน last committed seq เป็นขอบบน snapshot และคืนใน response
- หน้าต่อไปส่ง through_seq เดิมเสมอ เพื่อให้การดึงจบได้แม้มีข้อความใหม่เข้าตลอด; เลือก `after_seq < seq <= through_seq` เรียงเพิ่มขึ้น
- `after_seq > through_seq`, through_seq สูงกว่าข้อมูลที่ commit แล้ว หรือ after_seq สูงกว่า last committed seq → 400 `INVALID_CURSOR`; อย่าล้างข้อมูล client อัตโนมัติ ให้ตรวจห้อง/environment และเริ่ม sync ใหม่อย่างชัดเจน

```json
{
  "items": [],
  "through_seq": "42",
  "next_after_seq": "42",
  "has_more": false
}
```

เมื่อมีรายการ next_after_seq คือ seq สุดท้ายที่คืน; เมื่อไม่มีรายการใช้ after_seq เดิม has_more เป็น true เมื่อยังมีข้อความใน snapshot ที่ไม่ได้คืน ไม่ขึ้นกับข้อมูลที่เกิดหลัง through_seq

หาก after_seq < through_seq แต่ไม่พบรายการตามเงื่อนไข ทั้งที่ v1 รับประกันลำดับต่อเนื่อง ให้ถือเป็นความผิดปกติและตอบ 503 `SYNC_INCONSISTENT` ห้ามเลื่อน cursor ข้ามข้อมูลที่ขาด

## 5. WebSocket protocol

เชื่อม `wss://<app-origin>/api/v1/ws` พร้อม cookie; ขอ subprotocol `chat.v1` และ server ต้องเลือก `chat.v1` ตรวจ session และ Origin ก่อน upgrade ไม่ใส่ token ใน query string

ก่อน upgrade ตอบ HTTP 401/403/429/503 ได้ Browser อาจไม่อ่านรายละเอียด handshake failure จึงเรียก GET session เมื่อต่อไม่ได้ซ้ำ ๆ; ไม่ตั้งสมมติฐานว่ารู้ status จาก WebSocket API

### Envelope และคำสั่ง

Client ใช้ request_id UUID ไม่ซ้ำภายใน connection เพื่อจับคู่ผล ไม่ใช่ idempotency key ของข้อความ ทุกเฟรมเป็น JSON text

```json
{"type":"subscribe","request_id":"55555555-5555-4555-8555-555555555555","room_id":"22222222-2222-4222-8222-222222222222"}
```

```json
{"type":"subscribed","request_id":"55555555-5555-4555-8555-555555555555","room_id":"22222222-2222-4222-8222-222222222222","last_seq":"42"}
```

- ตรวจสมาชิก แล้วเปิด local subscription ก่อนอ่าน last_seq; จัดให้ subscribed เข้าคิวก่อน message.created ของห้องนั้นเสมอ
- last_seq เป็นเพียง snapshot สำหรับเทียบ ไม่เลื่อน cursor ของ client
- subscribe ห้องเดิมซ้ำได้ ตอบ subscribed อีกครั้งโดยไม่สร้าง subscription ซ้อน
- เหตุการณ์ก่อนเริ่มติดตามอาจไม่ถูกส่งสด แต่ต้องดึงผ่าน HTTP sync ได้

คำสั่งอื่น:

| Client | Server | ความหมาย |
| --- | --- | --- |
| `{"type":"unsubscribe","request_id":"<uuid>","room_id":"<uuid>"}` | `{"type":"unsubscribed","request_id":"<uuid>","room_id":"<uuid>"}` | เลิกติดตาม; ไม่เคยติดตามก็สำเร็จ ไม่เปิดเผยข้อมูลห้อง; ยกเลิกข้อมูลที่ยังไม่ส่งของห้องก่อน ACK ส่วนที่ส่งไปแล้วถอนคืนไม่ได้ |
| `{"type":"ping","request_id":"<uuid>"}` | `{"type":"pong","request_id":"<uuid>"}` | heartbeat ระดับแอป ทุก 25 วินาที; client รอ pong 10 วินาทีแล้วเชื่อมใหม่ |

### เหตุการณ์จากเซิร์ฟเวอร์

```json
{"type":"message.created","message":{"message_id":"33333333-3333-4333-8333-333333333333","room_id":"22222222-2222-4222-8222-222222222222","seq":"42","sender":{"user_id":"11111111-1111-4111-8111-111111111111","display_name":"Demo A"},"client_message_id":"44444444-4444-4444-8444-444444444444","text":"hello","created_at":"2026-10-30T10:00:00Z"}}
```

- ใช้ Message แบบเดียวกับ HTTP; อาจซ้ำ มาผิดลำดับ หรือหาย จึงไม่มี event ACK/replay ผ่าน WebSocket
- ตรวจ session/สมาชิกจากฐานข้อมูลก่อนอนุญาตส่งแต่ละข้อความ; เมื่อพบว่าถูกลบ ส่ง `subscription.revoked` พร้อม room_id, reason `ACCESS_REVOKED` แล้วหยุดติดตาม หากส่งไม่ได้ก็หยุดติดตามได้เลย
- notification การลบสมาชิกหายก็ยังต้องปฏิเสธข้อมูลใหม่ในการตรวจครั้งถัดไป; ข้อมูลที่อนุญาตหรือส่งออกไปแล้วอาจถึงผู้รับตาม ADR
- ฐานข้อมูลตรวจสิทธิ์ไม่ได้ให้หยุดส่งข้อมูลและปิด 1013; ห้ามใช้สิทธิ์เก่าเพื่อส่งต่อ
- event type ที่ไม่รู้จักให้ client ละเลยพร้อมเก็บ telemetry ที่ไม่มีข้อมูลอ่อนไหว; เฟรมเสียหรือผิด schema ให้เชื่อมใหม่และ sync

ข้อผิดพลาดของคำสั่ง:

```json
{"type":"error","request_id":"55555555-5555-4555-8555-555555555555","error":{"code":"ROOM_NOT_FOUND","message":"Room unavailable","retryable":false}}
```

เฟรมที่อ่าน request_id ไม่ได้ใช้ request_id เป็น null; error ไม่ได้หมายความว่า connection ปิดเสมอ

| Close code | ความหมาย/การตอบสนอง |
| --- | --- |
| 1000 | ปิดปกติ; logout ไม่ต้อง reconnect |
| 1001 | server drain/redeploy; reconnect และ sync |
| 1008 | รูปแบบ/นโยบายผิดซ้ำ; หยุด retry อัตโนมัติและแสดงข้อผิดพลาด |
| 1009 | เฟรมใหญ่เกินขีดจำกัด; แก้ client ก่อน retry |
| 1013 | คิวเต็ม/ตรวจฐานข้อมูลไม่ได้/ระบบไม่พร้อม; backoff, reconnect และ sync |
| 4401 | session หมดอายุ/ถูกเพิกถอน; ตรวจ GET session และให้ login ใหม่ |

กรณีขาดการเชื่อมต่อโดยไม่ได้ close frame ให้ถือว่าผลไม่แน่นอนและใช้ backoff ไม่ใช้ 1006 เป็นรหัสที่ server ส่ง

## 6. ขั้นตอน recovery ฝั่งเว็บ

1. เปิด socket แล้ว subscribe ห้อง รอ subscribed; buffer เหตุการณ์สดของห้องนั้น
2. เรียก sync ด้วย contiguous cursor ของอุปกรณ์/ผู้ใช้/ห้อง; ถ้ายังไม่มีใช้ 0 ไม่ใช้ last_seq จากรายการห้องแทน
3. กำหนด through_seq จาก sync หน้าแรกและใช้เดิมจน has_more เป็น false
4. รวม HTTP + live ด้วย message_id เรียงด้วย seq; เลื่อน cursor เฉพาะ seq ที่ต่อจากเดิมครบ ห้ามข้ามช่องว่าง
5. ถ้าพบ seq สูงกว่า cursor+1 ให้ sync เพิ่ม; ข้อมูลสดที่เกิน snapshot เก็บไว้รวมได้
6. ตรวจ sync ตามรอบทุก 15 วินาทีเมื่อหน้าเว็บ active แม้ socket ยังต่ออยู่ และทันทีเมื่อกลับมา active เพื่อพบ notification สุดท้ายที่หาย
7. reconnect ใช้ exponential backoff แบบ full jitter ตั้งแต่ช่วง 0–1 วินาที เพิ่มเพดานจนถึง 30 วินาที; reset เมื่อเชื่อมและ subscribe สำเร็จต่อเนื่อง 60 วินาที
8. buffer ฝั่งเว็บไม่เกิน 500 เหตุการณ์ต่อห้อง; ถ้าเต็มให้หยุด socket ของ connection นั้น ทิ้งเฉพาะ live buffer แล้ว reconnect/sync จาก cursor เดิม ห้ามทิ้ง pending sends หรือเลื่อน cursor
9. บันทึกข้อความที่รับกับ cursor ใน IndexedDB transaction เดียว; หากข้อมูลเก่าถูกล้างหรือไม่สอดคล้อง ให้เริ่มจาก 0 โดยไม่อ้างว่า cursor ที่เหลือหมายถึงมีข้อมูลครบ

IndexedDB pending มี `(user_id, room_id, client_message_id, text)`; unknown outcome คง pending แล้วลองรหัสเดิม ข้อผิดพลาดถาวรแสดงให้ผู้ใช้แก้ไข/ยกเลิก การแก้ text ถือเป็นคำสั่งใหม่และใช้รหัสใหม่

## 7. ข้อผิดพลาดและขีดจำกัด

HTTP error รูปแบบเดียวกัน ยกเว้น browser redirect ของ auth ที่สำเร็จ:

```json
{"error":{"code":"IDEMPOTENCY_CONFLICT","message":"Request key already used with different text","request_id":"server-generated-id","retryable":false,"details":[]}}
```

details เป็นรายการ `{field, code}` ของ validation ที่ไม่สะท้อนข้อมูลลับ; client ใช้ code ไม่ parse message

| HTTP | code ตัวอย่าง | พฤติกรรม |
| --- | --- | --- |
| 400 | INVALID_REQUEST, INVALID_CURSOR, UNSUPPORTED_PROTOCOL | แก้คำขอ ไม่ retry เดิมอัตโนมัติ |
| 401 | UNAUTHENTICATED | login ใหม่ |
| 403 | CSRF_FAILED, ORIGIN_NOT_ALLOWED, ACCOUNT_NOT_ALLOWED | ปฏิเสธ; CSRF ใหม่รับจาก session ได้แต่ห้าม retry ไม่สิ้นสุด |
| 404 | ROOM_NOT_FOUND | หยุดติดตามและหยุดส่งห้องนั้น |
| 409 | IDEMPOTENCY_CONFLICT | ห้ามใช้รหัสเดิมกับเนื้อหาใหม่ |
| 413 | PAYLOAD_TOO_LARGE | ลดขนาดคำขอ |
| 415 | UNSUPPORTED_MEDIA_TYPE | ใช้ application/json |
| 422 | VALIDATION_FAILED | แก้ field ตาม details |
| 429 | RATE_LIMITED | เคารพ Retry-After หน่วยวินาที |
| 500 | INTERNAL_ERROR | ผลอาจไม่แน่นอน; retry ส่งด้วยรหัสเดิมและ backoff |
| 503 | DEPENDENCY_UNAVAILABLE, SYNC_INCONSISTENT | ไม่อ้างสำเร็จ; retry พร้อม backoff และรหัสเดิม |

ไม่ส่ง SQL, stack trace หรือชื่อ secret ให้ client; 429/503 ให้ Retry-After เมื่อระบุได้

ค่าเริ่มต้นสำหรับเดโม:

| รายการ | ขีดจำกัด |
| --- | --- |
| HTTP request body | 16 KiB |
| ข้อความ text | 4,096 UTF-8 bytes |
| WebSocket frame | 16 KiB |
| subscription ต่อ socket | 50 ห้อง; เกินตอบ WS error SUBSCRIPTION_LIMIT |
| คิวขาออกต่อ socket | ไม่เกิน 256 เฟรม หรือ 1 MiB แล้วแต่ว่าอย่างใดถึงก่อน; เต็มให้ปิด 1013 และกู้ด้วย sync |
| ส่งข้อความต่อผู้ใช้ | 5 คำขอ/วินาที burst 10 รวม replay ทุก replica |
| อ่าน API ต่อผู้ใช้ | 20 คำขอ/วินาที burst 40 ทุก replica |
| handshake WebSocket | 10 ครั้ง/นาทีต่อผู้ใช้ burst 5; เกิน HTTP 429 ก่อน upgrade |
| คำสั่ง WebSocket | 5 เฟรม/วินาที burst 10 ต่อ connection; เกินตอบ RATE_LIMITED แล้วปิด 1008 หากทำซ้ำ |

rate limit ข้าม replica ต้องใช้ shared atomic state แยก prefix/environment จาก Pub/Sub; ใช้ Valkey ที่มีอยู่ได้ ไม่เพิ่มบริการใหม่ ถ้า limiter ใช้ไม่ได้ให้ 503 สำหรับเส้นทางที่ต้องจำกัด อย่า fallback เป็น per-replica โดยไม่บอก จำนวนข้างต้นเป็นเพดานต่อผู้ใช้ ไม่ใช่เป้าโหลดรวมของชั้นเรียน

## 8. Health และข้อตกลงกับระบบคลาวด์

| Endpoint | ผลและผู้เรียก |
| --- | --- |
| `GET /health/live` | 200 เมื่อ process ยังทำงาน; ไม่ตรวจ dependency; ใช้ตรวจ container |
| `GET /health/ready` | 200 เมื่อไม่ drain, schema รองรับ, PostgreSQL และการเชื่อม/subscribe Pub/Sub พร้อม; ถ้าไม่พร้อม 503; ใช้ ALB health check |

Health endpoint ไม่ต้อง login แต่คืนเพียง `{status:"ok"}` หรือ `{status:"unavailable"}` ไม่เปิดรายละเอียดระบบ; ไม่ expose ผ่าน CloudFront โดยไม่จำเป็น Worker ใช้ health command แยก ตรวจ loop/heartbeat และ dependency ไม่ผ่าน ALB

- Pub/Sub ล่มแต่ฐานข้อมูลยังดี: POST ที่เข้าแอปพร้อมรับคำขอแล้วอาจ commit และ ACK ได้ พร้อม outbox รอ; readiness ของ replica ที่รับสดไม่ได้เป็น 503 และ ALB อาจหยุดส่งคำขอใหม่ client retry รหัสเดิมได้
- ECS drain: readiness เป็น 503 ก่อนหยุดรับงานใหม่; รอ HTTP transaction ที่รับไว้ให้เสร็จภายใน grace period ปิด socket 1001 แล้วคืน/ปล่อย lease ของ worker ตามกติกา
- ไม่ใช้งาน sticky session เป็นหลักประกันความถูกต้อง; cookie/session และข้อความต้องใช้กับ replica อื่นได้
- คน A ดูแล schema/HTTP/WebSocket server, task health, worker, metrics และการทดสอบข้อมูลบน ECS/RDS/ElastiCache
- คน B ดูแล client protocol, CloudFront/ALB routing + forwarding + timeout, Cognito configuration และ CI/CD ที่ทดสอบสัญญานี้
- ทั้งคู่ตกลง timeout ทั้งเส้นทางให้รองรับ heartbeat และทดสอบ HTTPS/WSS กับ cookie จริงบน AWS; ห้ามถือว่าทดสอบ local ผ่านแล้วเท่ากับผ่านคลาวด์

## 9. รายการตรวจรับสัญญา

- [ ] ยิงคำขอเหมือนกันพร้อมกัน 100 ครั้ง ได้ message_id/seq เดียว และข้อความ/outbox เพียงรายการเดียว
- [ ] รหัสซ้ำแต่ text ต่างได้ 409; ผู้ถูกลบจากห้อง replay แล้วได้ 404
- [ ] ลบสมาชิกแข่งกับส่ง/subscribe/read/live แล้วเป็นไปตามเงื่อนไขการตรวจหลัง removal commit
- [ ] commit สำเร็จแต่ HTTP response หาย แล้ว retry ได้ข้อมูลเดิม
- [ ] live มาถึงก่อน HTTP, ซ้ำ และมาผิดลำดับแล้ว UI ไม่มีข้อความซ้ำ/ไม่เลื่อน cursor ข้ามช่องว่าง
- [ ] subscribe แข่งกับข้อความใหม่ และ sync หลายหน้าขณะมีข้อความเข้าไม่หยุด ยังดึง snapshot จบและกู้ครบ
- [ ] notification สุดท้ายหายทั้งที่ socket อยู่ แล้ว periodic sync กู้ได้
- [ ] คิว server/client เต็มและย้าย replica แล้วกู้ข้อความที่ ACK แล้วครบ
- [ ] session หมดอายุ/logout-all/ฐานข้อมูลตรวจสิทธิ์ไม่ได้แล้วไม่ส่งข้อมูลส่วนตัวเพิ่มหลังตรวจพบ
- [ ] สองผู้ใช้หรือสองอุปกรณ์ไม่ปน pending/cursor; reload ระหว่างส่งใช้ client_message_id เดิม
- [ ] CloudFront ไม่ cache API/auth/error และส่ง cookie/query/header/WebSocket upgrade ครบ
- [ ] rolling deployment, rate limit ข้าม replica และ readiness ตอน dependency ล่มมีหลักฐานจริง

เอกสารนี้กำหนด wire contract และค่าเริ่มต้นสำหรับพัฒนา ส่วน lock order, outbox lease duration, encryption/key rotation และ Terraform timeout ที่เลือกจริงต้องระบุใน implementation/runbook พร้อมหลักฐานก่อนตรวจรับ ไม่อ้างว่ามีแล้วจากการเขียนเอกสาร
