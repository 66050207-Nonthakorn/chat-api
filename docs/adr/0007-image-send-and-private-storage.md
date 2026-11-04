---
status: accepted
---

# ส่งรูปพร้อมข้อความในคำขอเดียวและอ่านผ่าน API

ผู้ใช้ยืนยันเมื่อ 2026-11-04 ให้เพิ่ม multipart ใน endpoint ส่งข้อความเดิมแทน upload API แยก เพื่อให้หนึ่งรูปขนาดไม่เกิน 5 MiB ใช้ขั้นตอน preview → Send → ยืนยันข้อความตามเดิม เก็บไฟล์ใน private S3 บน AWS และใช้ shared S3-compatible storage ใน local เพื่อให้หลาย API replica เข้าถึงไฟล์เดียวกันได้ แลกกับการส่ง bytes ผ่าน API และการประมวลผลรูปที่ต้องจำกัดทรัพยากร

เลือกอ่านรูปผ่าน API ที่ตรวจ session/สมาชิกปัจจุบันทุกคำขอ แทน public หรือ presigned URL เพื่อรักษาสิทธิ์ตาม ADR 0002 และ 0006 เก็บ metadata/fingerprint กับข้อความใน PostgreSQL และบันทึกข้อความพร้อม outbox หลังเขียน object สำเร็จ เนื่องจาก S3 กับ PostgreSQL ไม่ใช่ transaction เดียวกัน จึงต้องมี ledger ภายในและ cleanup สำหรับไฟล์ที่ยังไม่ผูกข้อความเกิน 24 ชั่วโมง โดย cleanup แข่งกับการส่งข้อความไม่ได้และห้ามลบรูปที่ส่งสำเร็จแล้ว

**สถานะการส่งมอบ:** Backend พัฒนาและทดสอบกับ PostgreSQL/Valkey/local S3-compatible storage แล้ว รวม compatibility rollout schema 1 → 2, upload deadline และ streaming read; ยังไม่ได้สร้าง/deploy ทรัพยากร AWS ฟีเจอร์ frontend แยกงาน ดู [คู่มือรูปภาพ](../image-messages.md), [spec](../../.scratch/image-messages/spec.md) และ [ผลตรวจ ticket 03](../../.scratch/image-messages/issues/03-harden-image-runtime.md)
