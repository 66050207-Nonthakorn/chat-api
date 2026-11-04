# Image messages — แผนที่การออกแบบ

## Notes

- เริ่มออกแบบวันที่ 2026-11-04 ด้วย grill-with-docs; backend implementation ปิดแล้วใน ticket 02 ส่วน frontend ยังแยกเจ้าของ
- ขอบเขต backend เดิมไม่รวม attachments: [spec เดิม](../chat-backend/spec.md)
- ข้อมูลและคำถามของฟีเจอร์อยู่ใน [spec.md](spec.md)
- บทสัมภาษณ์ที่จบแล้ว: [01-design-interview](issues/01-design-interview.md)

## Decisions-so-far

- Q1: ผู้ใช้ยืนยันให้ส่งรูปในทั้ง DM และกลุ่ม ไม่รวมรูปโปรไฟล์และไฟล์แนบทั่วไปในฟีเจอร์นี้
- Q2: ยืนยันหนึ่งรูปต่อข้อความ คำบรรยายไม่บังคับ และ preview ที่เอารูปออกได้ก่อนส่ง
- Q3: ยืนยัน JPEG/PNG/WebP ไม่เกิน 5 MiB ต่อรูป ไม่รวมภาพเคลื่อนไหว, SVG, วิดีโอ และไฟล์ชนิดอื่น
- Q4: ยืนยันสมาชิกปัจจุบันอ่านรูปในประวัติได้ทั้งหมด ผู้ถูกนำออกขอรูปใหม่ไม่ได้ ไม่มี public URL ถาวร
- Q5: ยืนยันเก็บเฉพาะภาพที่ปรับแนว ลบ metadata และจำกัดพิกเซล ไม่เก็บต้นฉบับฝั่งเซิร์ฟเวอร์หลังประมวลผล
- Q6: ยืนยันเริ่มอัปโหลดเมื่อกด Send แสดง pending จน commit ข้อความ และ Retry ไม่สร้างข้อความซ้ำ
- Q7: ยืนยันเก็บรูปที่ส่งแล้วตลอดช่วงโครงการตามประวัติข้อความ ไม่มี expiry หรือฟีเจอร์แก้ไข/ลบใหม่
- Q8: ยืนยันกู้ pending รูป/คำบรรยายหลัง refresh แยกบัญชี ล้างหลังส่งสำเร็จ/logout และแจ้งให้เลือกรูปใหม่หาก browser storage ใช้ไม่ได้
- Q9 และการยืนยันสุดท้าย: ผู้ใช้ยืนยันแบบออกแบบทั้งหมดเมื่อ 2026-11-04 รวมด้านยาว 2,560 px, transparency, viewer และ caption limit เดิม
- ใช้ single multipart Send, private S3/shared local compatible storage, API อ่านรูปพร้อมตรวจสิทธิ์ และ cleanup candidate ที่ไม่ได้ส่งเกิน 24 ชั่วโมงตาม [ADR 0007](../../docs/adr/0007-image-send-and-private-storage.md)
- เอกสารฉบับสุดท้ายอยู่ใน directory และ [Notion](https://www.notion.so/40e766880300818bb5ecf5fa73f0154c)
- สรุปใน [Notion](https://www.notion.so/40e766880300818bb5ecf5fa73f0154c) สร้างและอ่านกลับยืนยันแล้ว พร้อมลิงก์จากหน้าสถาปัตยกรรมและข้อสรุปการออกแบบเดิม
- Backend implementation และ `go build ./...` ผ่านตาม [ticket 02](issues/02-implement-backend.md); production image send gate ปิดโดยค่าเริ่มต้น
- เพิ่ม schema-1 compatibility rollout, upload read deadline/429 admission, streaming download และ shared-edge config/checker ตาม [ticket 03](issues/03-harden-image-runtime.md); integration บน PostgreSQL, Valkey และ local S3-compatible storage ผ่านแล้ว
- สิทธิ์และข้อแลกเปลี่ยนการไม่เก็บต้นฉบับ: [ADR 0006](../../docs/adr/0006-private-normalized-image-messages.md)
- นิยาม Image message บันทึกใน [GLOSSARY.md](../../GLOSSARY.md)

- Bruno image flow, PNG fixture และเอกสารฉบับปัจจุบันอยู่ใน [ticket 04](issues/04-bruno-and-docs.md); parser/script/fixture validation ผ่าน แต่ยังไม่ได้รัน Bruno flow จริงกับ API ในงานอัปเดตนี้

## Fog

- ไม่มีคำถามด้านขอบเขตค้างอยู่ แบบออกแบบได้รับการยืนยันแล้ว
- AWS และ shared CloudFront/ALB ยังไม่ได้ deploy/ทดสอบจริง; frontend upload, retry และ viewer รวมถึง capacity benchmark ยังแยกงาน
