# แผนใช้เครดิตฟรี ค่าใช้จ่าย และการล้างทรัพยากร

> ฉบับละเอียดก่อนย่อ Notion เก็บเมื่อ 2026-10-28
> เอกสารแผนและข้อสรุป ไม่ใช่หลักฐานว่าพัฒนา ทดสอบ หรือ deploy สำเร็จ
> ที่มา: [หน้า Notion](https://www.notion.so/4037668803008168a642f237bad34221) — ปัจจุบันเป็นฉบับย่อ

**สถานะ: ยืนยันเป้าหมายจ่ายเองใกล้ศูนย์ แต่ยังไม่ได้สมัคร AWS**
ปรับปรุงเมื่อ 2026-10-25 คงสถาปัตยกรรมที่เลือกไว้ เริ่มจากเอกสารและการพัฒนาบน local

## แผนบัญชีและขอบเขตการใช้เงิน

เลือก **AWS Free account plan** โดยต้องตรวจสิทธิ์จริงหลังสมัคร ห้ามอัปเกรด Paid plan หรือเปลี่ยนสถาปัตยกรรมโดยไม่ทบทวนกับเจ้าของ ข้อเสนอเดิมให้สำรองเงินส่วนตัว USD 50 ไม่ได้ถูกยอมรับ จึงไม่ใช่งบประมาณที่อนุมัติ

ตามข้อมูล AWS ที่ตรวจแล้ว ผู้สมัครใหม่ที่มีสิทธิ์ได้รับเครดิตเริ่มต้น USD 100 และทำกิจกรรมเพื่อรับเพิ่มได้ถึง USD 100 Free plan สิ้นสุดเมื่อครบหกเดือนหรือเครดิตหมดแล้วแต่ว่าอย่างใดถึงก่อน บัญชีปิดเมื่อแผนสิ้นสุดหากไม่อัปเกรด จึงต้องส่งออกหลักฐานก่อนหมดอายุ บางบริการ/ฟีเจอร์มีข้อจำกัด และบางการดำเนินการกับบัญชีทำให้อัปเกรดอัตโนมัติได้ ดู [เงื่อนไขแผนบัญชี AWS](https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/free-tier-plans.html)

ข้อมูลข้างต้นเป็นกฎโครงการที่เผยแพร่ ไม่ใช่สิทธิ์บัญชีที่ตรวจแล้ว หลังสมัครต้องยืนยันแผน ยอดเครดิต วันหมดอายุ region บริการ quota และวันสาธิต ห้ามเพิ่ม Organizations/Control Tower หรือข้อกำหนดที่ทำให้เป็น Paid plan โดยถือเป็นเพียงขั้นตอนย่อยของ Terraform

## ประมาณการใช้เครดิตตามมิเตอร์

Fargate, RDS, ElastiCache, ALB และ NAT ไม่ได้ฟรีถาวร แต่คิดค่าบริการที่นำเครดิตมาชำระได้ตามสิทธิ์ เป้าจ่ายเองศูนย์จึงขึ้นกับ Free plan ที่ยังมีผลและบริการที่ใช้ได้ ไม่ใช่การถือว่า budget alert จำกัดยอดจ่ายได้

Region สำหรับคำนวณชั่วคราวคือ Singapore หรือ ap-southeast-1 **ยังไม่ได้เลือกเป็นข้อสรุป** ตรวจราคาจากรายการทางการเมื่อ 2026-10-25 และต้องตรวจใหม่ก่อนใช้จริง

| รายการ | พัฒนา USD/ชั่วโมง | สาธิตความทนทาน USD/ชั่วโมง |
| --- | --- | --- |
| API 2 task ตัวละ 0.5 vCPU / 1 GiB | 0.06162 | 0.06162 |
| Worker 1 task ขนาด 0.25 vCPU / 0.5 GiB | 0.015405 | 0.015405 |
| RDS db.t4g.small | 0.051 แบบ Single-AZ | 0.102 แบบ Multi-AZ รวม standby แล้ว |
| RDS gp3 20 GB เฉลี่ยตามชั่วโมง | 0.00378 | 0.00756 |
| Valkey cache.t4g.micro | 0.0192 จำนวน 1 node | 0.0384 จำนวน 2 node |
| NAT เฉพาะค่ารายชั่วโมง | 0.059 จำนวน 1 ตัว | 0.118 จำนวน 2 ตัว |
| Public IPv4 ของ ALB และ NAT ตามสมมติฐาน | 0.015 รวม 3 IP | 0.020 รวม 4 IP |
| ALB และเผื่อการใช้เฉลี่ย 1 LCU | 0.0332 | 0.0332 |
| รวมองค์ประกอบหลัก | ประมาณ 0.26 | ประมาณ 0.40 |

เป็นขนาดสำหรับประมาณราคา ไม่รับรองผลโหลด ต้องตรวจว่าแอปและ collector ของ OTel อยู่ในหน่วยความจำที่จัดสรร ราคาฐานข้อมูล Multi-AZ รวม standby แล้ว ห้ามคูณซ้ำ ALB อาจใช้ IP/LCU มากขึ้นตามโหลด ค่า NAT ประมวลผลข้อมูลเพิ่ม USD 0.059/GB สำหรับราคาที่ตรวจใน Singapore ส่วน storage ใช้ฐาน 730 ชั่วโมงต่อเดือนในการคำนวณ

ตัวอย่าง **พัฒนา 24 ชั่วโมง + สาธิต 12 ชั่วโมง = USD 10.95 สำหรับองค์ประกอบหลัก** เผื่อการใช้เครดิตรวมประมาณ **USD 20–30** เมื่อรวม log/trace/metric ปริมาณน้อย traffic, ECR/S3, secrets, snapshot/DNS, task ระหว่าง rolling และความคลาดเคลื่อน หากเพิ่มเป็น 48 + 24 ชั่วโมง ส่วนหลักประมาณ USD 21.90 และวงประมาณการเดิมรวมส่วนเผื่อเป็น USD 35–50 ทั้งหมดเป็นปริมาณใช้บริการ ไม่ใช่งบเงินสดส่วนตัวที่ตกลงแล้ว

หากเปิดสอง environment พร้อมกันต้องนับชั่วโมงทั้งคู่ รวมเวลาสร้าง รอ ซ้อม และล้างด้วย หากลืมเปิดแบบสาธิตค้างหนึ่งสัปดาห์ ส่วนหลักประมาณ USD 66.56 ก่อนรายการอื่น

## สมมติฐานและสิ่งที่ยังไม่รวม

- ใช้ Linux x86 แบบ on-demand ไม่ใช้ Spot หรือ reserved commitment ตรวจรุ่น ขนาด และ quota อีกครั้ง
- เดโมข้อความล้วน ผู้ใช้ที่ได้รับเชิญไม่กี่ราย ไม่ใช้ SMS หรือส่วนความปลอดภัย Cognito ที่เสียเพิ่ม
- จำกัด telemetry เก็บ log สั้น ใช้ trace sampling และ metric ที่ไม่มี label ต่อผู้ใช้/ข้อความ
- เก็บไฟล์ frontend/image ขนาดเล็ก และไม่ถือว่า allowance ฟรีของบัญชียังเหลือครบ
- ประมาณราคาเดิมสมมติว่ามีโดเมน/subdomain ใช้ได้ แต่ยังไม่อนุมัติซื้อโดเมน ต้องแก้เส้นทาง TLS ระหว่าง CloudFront กับ ALB โดยไม่เกิดการซื้อที่ไม่ตกลง การมี hostname frontend ของ AWS อย่างเดียวไม่ได้แก้ใบรับรอง origin ทุกกรณี
- ยังไม่รวมภาษี การแปลงสกุลเงิน การซื้อโดเมน GitHub runner/plan แบบเสียเงิน snapshot ขนาดใหญ่ หรือเครื่องสร้างโหลดบนคลาวด์
- เครดิต CPU ของ RDS เมื่อ burst ต่อเนื่อง และ log/fan-out/traffic สูงกว่าคาดทำให้ใช้เครดิตเพิ่มได้
- ยังไม่ได้ตรวจยอดเงินจริง สิทธิ์บริการ และ allowance คงเหลือของบัญชีใด

## ขั้นตอนเปิดใช้งานแต่ละรอบ

1. ทำ local และผ่านเกณฑ์ความถูกต้องก่อน
2. ตรวจว่า Free plan ยังมีผล วันหมดอายุ เครดิตคงเหลือ บริการที่ใช้ได้ และวงประมาณการของรอบนั้น
3. ตรวจ Terraform plan และนโยบายข้อมูลที่เก็บหลังจบ จำกัด task สูงสุดและเวลาทดสอบ กำหนดเส้นตาย cleanup
4. สร้างเฉพาะ environment ที่ต้องใช้ GitHub ต้องไม่สร้าง dev environment ที่ไม่มีอยู่โดยอัตโนมัติ
5. เก็บชั่วโมงจริงและตรวจการใช้เครดิตหลังรอบ ระบบ billing อาจแสดงช้า
6. ส่งออกหลักฐาน/ข้อมูลที่อนุญาต แล้ว teardown ทรัพยากรที่รันทันที
7. ตรวจรายการตกค้าง: NAT, ALB, EIP, RDS, cache, task, endpoint แบบเสียเงิน, S3, image, secret, DNS zone, log และ snapshot
8. ยืนยันว่าสิ่งที่เหลือเป็นสิ่งที่ตั้งใจเก็บและทราบต้นทุน ทำอีกครั้งหลังเดโมสุดท้าย

หยุด ECS task อย่างเดียวไม่หยุดค่า NAT/ALB/cache และหยุด RDS แล้วยังมีค่า storage ใช้ Terraform ล้างโดยระบุนโยบายข้อมูลชัดเจน ไม่ถือว่าจำนวน task ศูนย์เท่ากับค่าใช้จ่ายศูนย์

AWS Budgets แจ้งยอดจริงและยอดคาดการณ์ได้ แต่ล่าช้าและ **ไม่ใช่เพดานค่าใช้จ่ายแบบตัดทันที** ใช้ร่วมกับเงื่อนไขบัญชี การจำกัดรอบ และ cleanup หากบริการขออัปเกรด Paid plan ให้หยุดขั้น deploy นั้นแล้วนำทางเลือกมาพิจารณา โดยทำ local ต่อได้

## แหล่งราคาและเงื่อนไข

- [รายการราคา Fargate ใน Singapore](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonECS/current/ap-southeast-1/index.json)
- [รายการราคา RDS ใน Singapore](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonRDS/current/ap-southeast-1/index.json)
- [รายการราคา ElastiCache ใน Singapore](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonElastiCache/current/ap-southeast-1/index.json)
- [รายการราคา ELB ใน Singapore](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSELB/current/ap-southeast-1/index.json)
- [รายการราคา EC2/NAT ใน Singapore](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonEC2/current/ap-southeast-1/index.csv)
- [ราคา VPC และ IPv4](https://aws.amazon.com/vpc/pricing/)
- [ราคา CloudWatch](https://aws.amazon.com/cloudwatch/pricing/), [Secrets Manager](https://aws.amazon.com/secrets-manager/pricing/), [Cognito](https://aws.amazon.com/cognito/pricing/) และ [CloudFront](https://aws.amazon.com/cloudfront/pricing/pay-as-you-go/)
- [เงื่อนไข Free account plan](https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/free-tier-plans.html)
- [พฤติกรรมการแจ้งเตือน AWS Budgets](https://docs.aws.amazon.com/cost-management/latest/userguide/budgets-managing-costs.html)

