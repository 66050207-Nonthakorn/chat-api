---
status: accepted
---

# โครงสร้างพื้นฐาน AWS และการส่งมอบ

เลือก ECS/Fargate, RDS PostgreSQL และ node-based ElastiCache พร้อม private backend เพื่อสาธิตโครงสร้างพื้นฐานที่สร้างซ้ำด้วย Terraform และส่งมอบผ่าน GitHub Actions/OIDC โดยเปิด AWS เฉพาะช่วงที่ต้องใช้

- **บันทึก:** 2026-10-28 จากข้อสรุปเดิมในบทสนทนา/Notion ไม่ใช่การตัดสินใจใหม่
- **สถานะการพัฒนา:** ยังไม่มีผลตรวจรับยืนยันในเอกสารต้นทาง

## ข้อสรุปและข้อแลกเปลี่ยน

- **D19 — ECS/Fargate:** ให้ AWS ดูแล compute ของ container แทนการจัดการ host EC2 หรือ Kubernetes
- **D20 — RDS PostgreSQL:** ฐานข้อมูล PostgreSQL แบบ managed แทน Aurora หรือดูแลฐานข้อมูลเอง
- **D21 — แยกรูปแบบพัฒนากับสาธิต:** พัฒนาใช้ Single-AZ RDS และสาธิตความทนทานใช้ Multi-AZ พร้อม standby ภายใต้สิทธิ์และเครดิตที่ต้องตรวจจริง
- **D22 — ElastiCache แบบ node:** เลือก topology ชัดเจนแทน serverless หรือ self-hosted ค่า estimate ใช้ Valkey เป็นข้อเสนอ ส่วน engine/version ขนาดและจำนวน replica ต้องยืนยัน
- **D23 — Rolling deployment:** ค่อยแทน task ที่พร้อมใช้งาน รองรับเวอร์ชันซ้อนกัน rollback และ reconnect ไม่รับรองว่า socket ไม่หลุด
- **D24 — Terraform:** สร้างโครงสร้างพื้นฐานจากนิยามที่ตรวจแผนก่อนใช้ได้ แทน CDK หรือ CloudFormation ตรง ๆ ต้องป้องกัน state และแยก environment
- **D25 — Backend อยู่ private subnet:** รับผ่าน ALB สาธารณะ ข้อมูลอยู่ private และต้องจัดเส้นทางออกซึ่งมีต้นทุน NAT/endpoint
- **D26 — Svelte + TypeScript + Vite:** เรนเดอร์ในเบราว์เซอร์และเผยแพร่ไฟล์ static ไปยัง S3 + CloudFront ตามข้อสรุปเดิม เปลี่ยนจาก React เป็น Svelte ตามที่เจ้าของเลือกเมื่อ 2026-10-28
- **D27 — S3 + CloudFront:** แยกการปล่อย frontend ใช้ origin เดียวผ่านเส้นทาง API/auth/WS ไป ALB ต้องตั้ง cache, cookie และ header ให้ถูกต้อง แทนเสิร์ฟไฟล์จาก Go
- **D29 — GitHub Actions:** pipeline อยู่กับ repository GitHub ใช้ OIDC รับสิทธิ์ AWS ชั่วคราวแบบจำกัดขอบเขต
- **D30 — Dev อัตโนมัติ สาธิตเลื่อนด้วยมือ:** deploy พัฒนาเมื่อ environment มีอยู่ ใช้ artifact เดิมที่ผ่านแล้วไปสาธิต ไม่สร้าง environment เพราะ push โค้ด
- **D31 — ระบบติดตามแบบ managed ของ AWS:** CloudWatch สำหรับ log/metric/dashboard/alarm และ OpenTelemetry สำหรับ trace ที่ส่งไป AWS ต้องจำกัด sampling, retention และจำนวน label การส่ง trace ยังเป็นแผน ไม่ใช่พฤติกรรมที่ ticket 07 ส่งมอบ: ขณะนี้ backend มี structured logs และ metrics ผ่าน CloudWatch แต่ยังไม่มี OpenTelemetry SDK/exporter หรือ collector จึงห้ามอ้างว่ามี trace coverage; กำหนด pipeline, sampling, retention และ label ก่อนเริ่มงาน trace แยกต่างหาก
- **D32 — จำนวน task คงที่ก่อน autoscaling:** วัดก่อนตั้ง policy ที่มีเพดาน แยก API กับ worker เคารพขีดจำกัด connection ฐานข้อมูล และระบาย socket เมื่อ scale-in
- **D40 — วัดเป็นขั้น:** พื้นฐาน 100 connections / 10 ข้อความต่อวินาที / กลุ่มไม่เกิน 20 คน ทดลองเพิ่มเป็น 1,000 / 100 / 100 ตามลำดับ เป็นเป้าทดสอบ ไม่ใช่ผลสำเร็จหรือ SLA
- **D41 — Local-first และเปิด AWS เมื่อต้องใช้:** ใช้ช่วง integration ซ้อม และสาธิตแล้วล้างทรัพยากร ระบบต้องรัน local ได้โดยไม่มี AWS

## เงื่อนไขที่ต้องรักษา

- ส่วนหน้าใช้ Svelte + TypeScript + Vite; static ผ่าน private S3/CloudFront ไม่เพิ่ม SSR server
- API/auth/WS ผ่าน ALB ภายใต้ origin เดียว; ไม่ cache ข้อมูลส่วนตัว
- Go API/worker ใช้ code/image/model ร่วม แต่รันแยก ECS service
- Rolling รองรับเวอร์ชันซ้อน/drain/reconnect; ไม่รับรอง socket ไม่หลุด
- Migration งานควบคุมครั้งเดียว แบบ expand/migrate/contract
- เริ่ม fixed capacity ก่อน bounded autoscaling; จำกัด DB connections รวม
- Engine/version/size/region/TLS และสิทธิ์บริการยังต้องยืนยัน

## รายละเอียดประกอบ

- [สถาปัตยกรรมระบบและการไหลของข้อมูล](references/01-architecture-and-data-flow.md)
- [แผนใช้เครดิตฟรี ค่าใช้จ่าย และการล้างทรัพยากร](references/05-aws-cost-and-cleanup.md)
- [รายการเตรียมพร้อมและรายละเอียดที่ต้องยืนยัน](references/06-readiness.md)

