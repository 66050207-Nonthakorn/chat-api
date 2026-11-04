# บันทึกการตัดสินใจและรายละเอียดโครงการ

- **Notion:** [ภาพรวมฉบับสั้น](https://www.notion.so/40376688030080eab849d47ee2c5ba29) สำหรับอ่านเร็ว ใช้ bullet/Diagram
- **โฟลเดอร์นี้:** เหตุผล ข้อแลกเปลี่ยน และรายละเอียดฉบับก่อนย่อ เก็บเมื่อ 2026-10-28
- **สถานะ:** ยืนยันการเลือกแบบแล้ว แต่ไม่ได้ยืนยัน implementation, tests, AWS account/credits, deployment หรือ benchmark
- **Frontend ปัจจุบัน:** Svelte + TypeScript + Vite
- ไม่ได้สร้าง ADR แยกทุกข้อ; รวม 41 ข้อสรุปเดิมตามเรื่อง พร้อมหมายเลข D เดิมเพื่อค้นหา

## ADR

- [แกนข้อความถาวรและการส่งสดข้าม replica](0001-durable-messaging-and-realtime.md)
- [ตัวตน Session และขอบเขตสิทธิ์สมาชิก](0002-identity-sessions-and-membership.md)
- [โครงสร้างพื้นฐาน AWS และการส่งมอบ](0003-aws-hosting-and-delivery.md)
- [สถานะไคลเอนต์และขอบเขตฟีเจอร์ระยะถัดไป](0004-client-state-and-deferred-features.md)
- [เดโมขั้นต่ำและการใช้เครดิต](0005-minimum-demo-and-credit-budget.md)
- [รูปในข้อความเป็นเนื้อหาส่วนตัวที่ผ่านการปรับรูป](0006-private-normalized-image-messages.md)
- [ส่งรูปพร้อมข้อความในคำขอเดียวและอ่านผ่าน API](0007-image-send-and-private-storage.md)

ฟีเจอร์รูปภาพ: [คู่มือ flow/API/rollout](../image-messages.md) และ [spec](../../.scratch/image-messages/spec.md) — backend และ local integration ผ่านแล้ว มี [Bruno image flow](../../bruno/cloud-chat-local/README.md) พร้อม fixture; frontend และ AWS จริงยังแยกงาน

- [สรุปแบบออกแบบรูปภาพใน Notion](https://www.notion.so/40e766880300818bb5ecf5fa73f0154c)

## เอกสารฉบับละเอียด

- [LINE เถื่อน - Cloud Computing](references/00-project-overview.md)
- [สถาปัตยกรรมระบบและการไหลของข้อมูล](references/01-architecture-and-data-flow.md)
- [แผนพัฒนาและเกณฑ์ตรวจรับ](references/02-implementation-and-acceptance.md)
- [ข้อสรุปการออกแบบและงานที่เลื่อนไประยะถัดไป](references/03-decisions-and-deferred-work.md)
- [แผนสาธิตและหลักฐานตามเกณฑ์คะแนน](references/04-demo-and-rubric.md)
- [แผนใช้เครดิตฟรี ค่าใช้จ่าย และการล้างทรัพยากร](references/05-aws-cost-and-cleanup.md)
- [รายการเตรียมพร้อมและรายละเอียดที่ต้องยืนยัน](references/06-readiness.md)

- รายละเอียดแผน/เกณฑ์คะแนน/ราคาเป็นเอกสารอ้างอิง ไม่ใช่ ADR เพิ่มเติมหรือหลักฐานตรวจรับ
- ฉบับละเอียดเก็บก่อนย่อ Notion และรวมการเปลี่ยน React → Svelte แล้ว
- วันที่สถานะ/ราคาในเอกสารเป็นวันที่ตรวจเดิม ไม่ได้ยืนยันใหม่ ณ วันที่คัดเก็บ
- `docs/aws-cost-estimate.md` เป็นการคำนวณเก่าภาษาอังกฤษ; ให้ใช้ [ฉบับสรุปละเอียดที่ปรับตามข้อสรุปล่าสุด](references/05-aws-cost-and-cleanup.md) สำหรับนโยบายงบ/เครดิต

## การแก้ไขต่อไป

- แก้เหตุผล/รายละเอียดในโฟลเดอร์นี้ แล้วปรับสรุป Notion ให้ตรง
- หากเปลี่ยนการตัดสินใจ ให้เพิ่ม ADR ถัดไปและระบุเอกสารที่ถูกแทนที่
- อย่าเปลี่ยนสถานะจาก “แผน” เป็น “ผ่าน” จนมีหลักฐานตรวจรับ

