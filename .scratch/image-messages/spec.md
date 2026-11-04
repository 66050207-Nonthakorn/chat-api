# ส่งรูปภาพในข้อความสนทนา

สถานะเอกสาร: แบบออกแบบยอมรับเมื่อ 2026-11-04; backend implementation, build และชุดทดสอบ Go พร้อม integration กับ PostgreSQL/Valkey/local S3-compatible storage ผ่านแล้ว Production image sending ปิดไว้โดยค่าเริ่มต้น ยังไม่ได้ deploy หรือทดสอบ AWS จริง ดูผลเพิ่มเติมใน ticket 03

ฉบับอ่านเร็วใน [Notion](https://www.notion.so/40e766880300818bb5ecf5fa73f0154c) อยู่ใต้โครงการ LINE เถื่อน - Cloud Computing และอ้างอิงแบบออกแบบฉบับนี้

## ความต้องการ

เพิ่มการส่งรูปภาพด้วยขั้นตอนเลือก → preview → Send ใช้ API/worker เดิม ผู้ใช้ยืนยันความเข้าใจร่วมกันแล้วและให้จัดทำเอกสารฉบับสุดท้ายทั้งใน directory และ Notion

## ขอบเขตที่ยืนยันแล้ว

- ส่งรูปได้ทั้งการสนทนาส่วนตัว (DM) และกลุ่มปิด
- รูปโปรไฟล์และไฟล์แนบทั่วไปเป็นงานแยก
- หนึ่งรูปต่อข้อความ พร้อมคำบรรยายที่ไม่บังคับ
- แสดง preview ก่อนส่ง และเอารูปออกจาก preview ได้
- รับ JPEG, PNG และ WebP ขนาดไม่เกิน 5 MiB ต่อรูป (5,242,880 bytes)
- ไม่รวมภาพเคลื่อนไหว, SVG, วิดีโอ และไฟล์ชนิดอื่นในเวอร์ชันแรก
- รูปใช้กฎสิทธิ์เดียวกับประวัติห้อง: สมาชิกปัจจุบันอ่านรูปทั้งหมดได้รวมรูปก่อนเข้าร่วม ผู้ถูกนำออกขอดูรูปจากระบบใหม่ไม่ได้ ไม่มีลิงก์สาธารณะถาวร และไม่สามารถเรียกคืนสำเนาที่ดาวน์โหลดแล้ว
- เก็บภาพสำหรับแชทหลังตรวจไฟล์จริง ปรับแนวภาพ ลบ metadata เช่น GPS และจำกัดด้านยาว 2,560 px โดยไม่ขยายภาพเล็ก คงความโปร่งใส ไม่เก็บไฟล์ต้นฉบับแบบ byte-for-byte ฝั่งเซิร์ฟเวอร์หลังประมวลผล
- preview อยู่ในเครื่องจนกด Send จากนั้นอัปโหลดและส่งข้อความ แสดง pending จนเซิร์ฟเวอร์บันทึกข้อความสำเร็จ หากล้มเหลวให้ Retry โดยไม่สร้างข้อความซ้ำ
- รูปที่ส่งแล้วเก็บเท่าประวัติข้อความตลอดช่วงโครงการ ไม่มีการหมดอายุอัตโนมัติ และไม่เพิ่มความสามารถแก้ไข/ลบข้อความในฟีเจอร์นี้
- กู้ข้อความรูปภาพรอยืนยันหลัง refresh: เก็บรูปและคำบรรยายแยกตามบัญชีในเบราว์เซอร์ ให้ผู้ใช้กด Retry หลังกลับมา ล้างสำเนาเมื่อยืนยันส่งสำเร็จหรือ logout หากเก็บในเบราว์เซอร์ไม่ได้ให้แจ้งและขอเลือกรูปใหม่ ไม่อ้างว่าข้อมูลในเครื่องมีความทนทานเท่าข้อความที่ส่งสำเร็จ
- เหตุผลเรื่องสิทธิ์และการเก็บเฉพาะภาพที่ผ่านการปรับรูป: [ADR 0006](../../docs/adr/0006-private-normalized-image-messages.md)
- เหตุผลเรื่อง API และ storage: [ADR 0007](../../docs/adr/0007-image-send-and-private-storage.md)

## สภาพปัจจุบัน

- ขอบเขต backend เดิมไม่รวม attachments: [spec เดิม](../chat-backend/spec.md)
- API ส่งข้อความรับ `client_message_id` กับ `text`; ยังไม่มี upload endpoint
- กฎข้อความและ schema ปัจจุบันต้องมีข้อความที่ไม่ว่าง จึงยังส่งข้อความที่มีเฉพาะรูปไม่ได้
- ต้องรักษาความหมายของการยืนยันหลัง commit, retry ด้วยรหัสเดิม, ลำดับต่อห้อง และการกู้ประวัติตาม ADR 0001
- ADR 0002 กำหนดให้สมาชิกปัจจุบันอ่านประวัติทั้งหมด และตรวจสิทธิ์จากข้อมูลสมาชิกปัจจุบัน ผู้ใช้ยืนยันให้นำกฎนี้มาใช้กับรูปด้วย แต่ยังไม่มี implementation ของฟีเจอร์รูป

## แบบออกแบบที่ยอมรับแล้ว

ใช้หนึ่งคำขอส่งข้อความ เก็บรูปแบบ private และอ่านผ่าน API เดิม รายละเอียดด้านล่างเป็นข้อกำหนดสำหรับ implementation ไม่ใช่พฤติกรรมที่ระบบปัจจุบันทำได้แล้ว

### การใช้งานและการแสดงผล

1. เลือกหรือวางรูปใน composer แสดง preview และคำบรรยาย เอารูปออกได้ ยังไม่อัปโหลด
2. เมื่อกด Send เก็บข้อมูล pending แยกบัญชีพร้อม `client_message_id` เดิมก่อนเริ่มส่ง ถ้าเก็บไม่ได้ให้แจ้งว่าต้องเลือกรูปใหม่หลัง refresh แต่ยังส่งในหน้าปัจจุบันได้
3. ส่งครั้งเดียวผ่าน HTTP แสดงสถานะกำลังส่ง และ progress ถ้า transport รองรับ ไม่แสดง sent ก่อน backend commit; ไม่เพิ่ม API อัปโหลดแยกให้ frontend
4. เมื่อยืนยันสำเร็จ รวมผล HTTP/live event ด้วย message ID และ seq ตามกฎเดิม แล้วล้างไฟล์ pending ในเครื่อง เมื่อผลลัพธ์ไม่แน่นอนคง pending และ Retry ด้วยข้อมูลเดิม
5. หลัง refresh กู้เฉพาะรายการของบัญชีที่ตรงกัน ให้กด Retry เอง ไม่ส่งอัตโนมัติ ล้างข้อมูลใน logout และตรวจบัญชีอีกครั้งก่อนส่ง
6. ภาพด้านยาวไม่เกิน 2,560 px โดยไม่ขยายภาพเล็ก คงความโปร่งใส แสดง preview ในห้อง กดเปิดภาพขนาดใหญ่และปิดกลับห้องได้ รองรับคีย์บอร์ดและข้อความอธิบายรูป
7. คำบรรยายใช้ `text` เดิม ว่างได้เมื่อมีรูป ไม่เกิน 4,096 UTF-8 bytes; ข้อความล้วนยังต้องไม่ว่าง

### HTTP และการจัดเก็บ

- ใช้ `POST /api/v1/rooms/{room_id}/messages` เดิม รองรับ `application/json` สำหรับข้อความล้วนตามสัญญาปัจจุบัน และเพิ่ม `multipart/form-data` สำหรับ `client_message_id`, `text` และไฟล์ `image` หนึ่งไฟล์
- รูปผ่าน backend เพื่อจำกัดขนาด ตรวจชนิดจริง ประมวลผล และบังคับสิทธิ์ ใช้ session, Origin และ CSRF เดิม ไม่เชื่อ MIME/นามสกุล/hash ที่ client อ้าง
- ใช้ private S3 สำหรับรูปบน AWS และ shared S3-compatible service หนึ่งตัวใน compose สำหรับ local เพื่อให้ API หลาย replica อ่านไฟล์เดียวกันได้ ไม่เก็บไฟล์ถาวรใน filesystem ของ API task และไม่ใช้ bucket ของ Terraform state หรือ frontend
- ไม่เพิ่ม image service, queue ประมวลผลรูป, thumbnail object หรือ API จัดการ attachment แยกในเวอร์ชันแรก
- `GET /api/v1/rooms/{room_id}/messages/{message_id}/image` ตรวจ session และสมาชิกปัจจุบันก่อน stream ไฟล์จาก storage ผ่าน API ส่ง `Cache-Control: no-store` และ `X-Content-Type-Options: nosniff` ไม่ redirect ไป presigned/public URL
- ไม่มี endpoint อ่านไฟล์ที่ยังไม่ผูกกับข้อความ สมาชิกห้องอื่นและผู้ไม่มีสิทธิ์อ่านไม่ได้ ID หรือ URL ของรูปไม่ใช่หลักฐานสิทธิ์
- response ข้อความเพิ่ม optional `image` ที่มี `url`, `content_type`, `byte_size`, `width`, `height` โดย URL เป็นเส้นทาง API ไม่เปิดเผย object key; history, sync และ live event ใช้รูปแบบเดียวกัน ไม่ส่ง binary ผ่าน WebSocket

### ขอบเขตประมวลผลภาพ

- จำกัดไฟล์ต้นทาง 5 MiB และทั้ง multipart request 6 MiB จำกัดจำนวน/ความยาวของ fields และปฏิเสธ field/ไฟล์ซ้ำที่ไม่อยู่ในสัญญา
- กำหนดเพดานรูปต้นทาง 25 ล้านพิกเซล และแต่ละด้านไม่เกิน 16,384 px ตรวจขนาดก่อน decode จำกัด image pipeline พร้อมกันหนึ่งรูปต่อ API task และปฏิเสธเมื่อเต็มด้วย 429 ใช้ request/read deadline 45 วินาทีให้สั้นกว่า CloudFront/ALB timeout ที่ 60 วินาที
- decoder ที่ยังทำงานต้องครอง processing slot จนจบ แม้ HTTP request ยกเลิกแล้ว ห้ามถือว่า timeout ยกเลิกงาน decode ได้เอง
- ปฏิเสธภาพเคลื่อนไหวและข้อมูลที่เสียหาย ตรวจ format จริงเป็น JPEG/PNG/WebP เท่านั้น
- ปรับ orientation ก่อน resize และ encode เป็นไฟล์ใหม่โดยไม่มี EXIF/XMP/metadata ต้นฉบับ ใช้ JPEG quality 85 สำหรับ JPEG และ WebP ที่ไม่มี alpha; PNG สำหรับ PNG และ WebP ที่มี alpha เพื่อคงความโปร่งใส
- ภาพที่ encode แล้วต้องไม่เกิน 5 MiB เช่นกัน หากเกินให้แจ้งว่าต้องใช้รูปเล็กลง ไม่เพิ่มวงจรบีบอัดหลายระดับ
- ต้องตรวจเลือก image processor ที่รองรับ orientation ในทุก container ที่รับจริงก่อน implement ห้ามสมมติว่า stdlib หรือ JPEG-only AutoOrientation รองรับ PNG/WebP ด้วย; ชื่อ library ไม่เป็นข้อผูกมัดของแบบออกแบบนี้

### ความทนทาน, retry และ cleanup

- คง key `(sender_user_id, room_id, client_message_id)` เดิม fingerprint ที่ server คำนวณใช้ชนิดเนื้อหา, คำบรรยายตรงตามที่ส่ง, SHA-256 และจำนวน bytes ของไฟล์ต้นทาง ไม่เก็บไฟล์ต้นฉบับถาวรเพื่อเทียบซ้ำ
- Retry ข้อมูลเดิมคืนข้อความเดิม เปลี่ยนคำบรรยาย รูป หรือเปลี่ยนระหว่างข้อความล้วนกับรูปภายใต้รหัสเดิมคืน conflict; ห้ามใช้ hash จาก client เป็นหลักฐาน
- เพิ่มข้อมูลรูปที่ผูกกับข้อความได้สูงสุดหนึ่งรูป พร้อม metadata และ fingerprint ให้ constraint อนุญาต text ว่างเฉพาะเมื่อมีรูป คงกฎข้อความล้วนเดิม
- เก็บ ledger ของ storage attempt ภายในฐานข้อมูล โดยบันทึก candidate และ object key ที่ไม่ใช้ซ้ำก่อน PUT; candidate ของ retry/concurrent attempt แต่ละครั้งอาจมี key ต่างกัน
- เมื่อ PUT สำเร็จ transaction ของข้อความตรวจ session/สมาชิกซ้ำ ล็อก candidate ที่ยังผูกได้ แล้วบันทึก message, image reference, seq และ outbox พร้อมกันก่อนตอบสำเร็จ
- ล้าง candidate ที่ยังไม่ผูกข้อความหลัง 24 ชั่วโมงโดยใช้ worker เดิม ไม่ลบรูปที่ส่งแล้ว และไม่ใช้ bucket lifecycle ที่ลบรูปตามอายุรวมกัน
- cleaner กับ transaction ส่งข้อความต้องล็อก candidate เดียวกัน: cleaner เปลี่ยนเป็นสถานะที่ผูกไม่ได้ก่อนลบ object ส่วน candidate ที่ผูกแล้วต้องไม่ถูกเลือก cleanup เก็บ tombstone และลองลบซ้ำเพื่อครอบคลุม PUT ที่จบช้าหรือผลลัพธ์ไม่แน่นอน
- หาก commit เกิด error ที่ไม่ทราบผล ห้ามลบไฟล์ทันที เพราะข้อความอาจ commit แล้ว ให้ cleanup พิจารณาจากสถานะที่บันทึกในฐานข้อมูล
- หาก storage อ่านไม่ได้หลังส่งแล้ว แสดง placeholder กับ Retry เฉพาะการโหลดรูป ไม่ทำให้ข้อความหายและไม่สร้างข้อความใหม่

### ขอบเขตการส่งมอบและความเข้ากันได้

- Backend: migration แบบเพิ่มก่อนปรับ constraint, multipart send, image processing/storage, protected read, fingerprints, history/sync/outbox/realtime, cleanup และ metrics ที่ไม่ใส่ข้อมูลส่วนตัว
- Frontend (ผู้รับผิดชอบแยกตามขอบเขตเดิม): Svelte composer/preview, multipart request, IndexedDB pending แยกบัญชี, Retry, image viewer และ placeholder
- Infra: private media bucket, IAM เฉพาะ media prefix ที่จำเป็น, shared local storage, same-origin routing และ timeouts/body limits ให้รองรับ image endpoint ทั้งนี้ infra เดิมยังไม่ได้สร้าง S3/CloudFront frontend ให้
- ทุก API/worker ที่อาจอ่านหรือส่ง event ต้องรองรับ image metadata ก่อนเปิดการส่งรูป ไม่ให้ replica เก่ามองข้อความรูปว่าเป็นข้อความว่างหรือทำ image field หาย production มี `enable_image_messages=false` จน backend rollout เสร็จ; เปิดแยกภายหลัง
- AWS apply ไม่ได้รวมอยู่ในงานนี้ และ frontend ยังคงเป็นผู้รับผิดชอบแยก

### เกณฑ์ตรวจรับ

- ส่งรูปอย่างเดียว/พร้อมคำบรรยายใน DM และกลุ่มได้ ตรวจใน HTTP, history, sync และ live ข้าม replica ได้ผลเดียวกัน
- ทดสอบรับ/ปฏิเสธชนิดจริง ขนาด 5 MiB, หลายไฟล์, ภาพเคลื่อนไหว, metadata/GPS, orientation ของ JPEG/PNG/WebP, alpha และ pixel/memory bounds
- Retry ปกติ, ผลตอบหาย, request พร้อมกัน และ worker/API restart ไม่สร้างข้อความซ้ำหรืออ้างไฟล์ที่ cleanup ลบไปแล้ว เปลี่ยนเนื้อหาด้วยรหัสเดิมได้ conflict
- การออกจากห้อง/ถอนสมาชิก/session หมดอายุระหว่าง upload ต้องปฏิเสธการส่งเมื่อ recheck; การอ่านใหม่ต้องตรวจสมาชิกปัจจุบัน และไม่มี public/presigned link ที่ข้าม API
- pending คืนหลัง refresh เฉพาะบัญชีเดิม, logout ล้างข้อมูล, กรณี browser storage ใช้ไม่ได้แจ้งให้เข้าใจ และ HTTP/live event ซ้ำไม่เพิ่มข้อความ
- cleanup ลบเฉพาะ candidate ที่ไม่ได้ส่งเกิน 24 ชั่วโมง ทดสอบแข่งกับ finalize รวม late PUT และ unknown commit ไม่ลบรูปที่ผูกข้อความแล้ว
- รัน local หลาย API replica ด้วย storage ร่วมและทดสอบ storage interruption; ทดสอบ AWS จริงแยกภายหลังเมื่อมี environment ที่อนุมัติ

### หลักฐานประกอบทางเทคนิค

- [AWS: presigned URLs](https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-presigned-url.html) — URL ยังใช้ได้ตามสิทธิ์และช่วงอายุ จึงเลือก proxy อ่านผ่าน API ให้ตรงกับกฎสมาชิกปัจจุบัน
- [AWS: S3 consistency](https://aws.amazon.com/s3/consistency/) — การเขียน object และ transaction ของ PostgreSQL ยังเป็นคนละขั้นตอน ต้องออกแบบ failure/cleanup เอง
- [Go: streaming multipart](https://pkg.go.dev/net/http#Request.MultipartReader) และ [MaxBytesReader](https://pkg.go.dev/net/http#MaxBytesReader) — ขอบเขตการอ่าน request
- [Go: image decoding security](https://pkg.go.dev/image#hdr-Security_Considerations) — ต้องจำกัดขนาดภาพต้นทางก่อน decode
- [WebP decoder](https://pkg.go.dev/golang.org/x/image/webp) และ [JPEG decoder](https://go.dev/src/image/jpeg/reader.go) — อย่าสมมติความสามารถ encode หรือ orientation จาก decoder
- [SeaweedFS](https://github.com/seaweedfs/seaweedfs) — ตัวเลือก local S3-compatible storage; เลือกและ pin version เมื่อ implement หลังตรวจ API ที่ใช้จริง

## สถานะและขั้นตอนถัดไป

ไม่มีคำถามด้านขอบเขตค้างอยู่ Backend code อยู่ใน `internal/imageproc`, `internal/imagestore`, `internal/httpapi/images.go`, migration 0002, outbox และ cleanup worker ทดสอบการ rollout schema 1 → 2, text/image replay, outbox, การอ่านแบบ stream และถอนสิทธิ์กับ Compose แล้ว ตาม [ticket 03](issues/03-harden-image-runtime.md) Shared CloudFront configuration มี fragment และ offline checker แต่ยังไม่ได้นำไปใช้กับ distribution จริง ฟีเจอร์ฝั่ง frontend ยังแยกอยู่ และ AWS/การวัด capacity จริงยังต้องตรวจภายหลัง
