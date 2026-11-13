resource "aws_security_group" "alb" {
  name   = "cloudchat-demo-alb"
  vpc_id = aws_vpc.chat.id
  ingress {
    from_port       = 443
    to_port         = 443
    protocol        = "tcp"
    prefix_list_ids = [data.aws_ec2_managed_prefix_list.cloudfront.id]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = [aws_vpc.chat.cidr_block]
  }
}
resource "aws_lb" "api" {
  name               = "cloudchat-demo"
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb.id]
  subnets            = aws_subnet.public[*].id
  idle_timeout       = 60
}
resource "aws_lb_target_group" "api" {
  name        = "cloudchat-demo-api"
  port        = 8080
  protocol    = "HTTP"
  target_type = "ip"
  vpc_id      = aws_vpc.chat.id
  health_check { path = "/health/ready" }
}
resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.api.arn
  port              = 443
  protocol          = "HTTPS"
  certificate_arn   = var.alb_certificate_arn
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  default_action {
    type = "fixed-response"
    fixed_response {
      content_type = "text/plain"
      status_code  = "403"
    }
  }
}
resource "aws_lb_listener_rule" "cloudfront" {
  listener_arn = aws_lb_listener.https.arn
  priority     = 10
  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.api.arn
  }
  condition {
    http_header {
      http_header_name = "X-Cloudchat-Origin-Verify"
      values           = [var.origin_shared_secret]
    }
  }
}
